package admin

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"github.com/lesomnus/darak/internal/auth"
)

// User is one managed account as the system currently reports it.
type User struct {
	Name     string   `json:"name"`
	UID      uint32   `json:"uid"`
	GID      uint32   `json:"gid"`
	Home     string   `json:"home"`
	Groups   []string `json:"groups"`
	FullName string   `json:"full_name,omitempty"`

	// SMB is the tdbsam side, which is a separate account from the unix one and
	// the only one that matters for reaching the server. Nil when Samba could
	// not be asked, which is different from "no account": the page says so
	// rather than rendering an absence as a disabled user.
	SMB *SMBAccount `json:"smb,omitempty"`

	// UsageBytes is what this uid occupies, or nil when unavailable.
	UsageBytes *uint64 `json:"usage_bytes,omitempty"`
}

// SMBAccount is the state of a user's Samba account.
type SMBAccount struct {
	Enabled bool `json:"enabled"`
}

// Group is one managed group and who is in it.
type Group struct {
	Name    string   `json:"name"`
	GID     uint32   `json:"gid"`
	Members []string `json:"members"`
}

// Where an inventory came from. The interface says what each one means in its
// own words, which is why this is a token and not a sentence.
const (
	SourceUsersync = "usersync"
	SourceNSS      = "nss"
)

// Inventory is the account picture the admin page renders.
type Inventory struct {
	Users  []User  `json:"users"`
	Groups []Group `json:"groups"`

	// Source is SourceUsersync or SourceNSS. It matters because the NSS listing
	// answers a weaker question: `getent passwd` with no argument asks each
	// module to enumerate, and winbind declines by default, so a
	// directory-served account is simply absent.
	Source string `json:"source"`

	// Warnings carry the reason a field is missing rather than letting it read
	// as data. An operator seeing every user with no SMB account should be told
	// that pdbedit failed, not left to conclude nobody can log in.
	Warnings []string `json:"warnings,omitempty"`
}

// Inventory collects the accounts within the managed id range.
//
// The list comes from `usersync export --format csv`, which is the same scan
// that decides what `plan` would do — so this page and the tool that manages
// the accounts can never disagree about who exists. Reading /etc/passwd here
// instead would be one more thing to keep in step, and would stop being true
// the day a directory service serves the names.
func (a *Admin) Inventory(ctx context.Context) (*Inventory, error) {
	inv := &Inventory{Users: []User{}, Groups: []Group{}}

	var users []User
	var groups []Group
	out, err := a.cfg.Runner.Run(ctx, "", a.cfg.UsersyncBin, "export", "--format", "csv")
	if err == nil {
		users, groups, err = parseExportCSV(strings.NewReader(out))
	}
	if err != nil {
		// usersync is how accounts are managed in production, but it is not the
		// only way this server is run: the local stack provisions accounts from
		// a shell script and ships no usersync at all. Falling back to NSS keeps
		// the page useful there, and keeps a broken usersync from turning "who
		// exists" into a blank screen.
		//
		// The two reasons are reported separately. "Not installed" describes the
		// deployment and needs no attention; "installed and failed" is something
		// an operator should look at, and only that one carries the message.
		users, groups = a.enumerate(ctx)
		inv.Source = SourceNSS
		if !notInstalled(err) {
			inv.Warnings = append(inv.Warnings, fmt.Sprintf("usersync could not be asked: %v", err))
		}
	} else {
		inv.Source = SourceUsersync
	}

	// Group membership per user, resolved the same way the helper pool resolves
	// it, so what the page shows is what a helper would actually be started
	// with. gid -> name is taken from the export so a directory-served group
	// still renders by name.
	byGID := map[uint32]string{}
	for _, g := range groups {
		byGID[g.GID] = g.Name
	}
	members := map[uint32][]string{}
	for i := range users {
		creds, err := a.cfg.Resolver.Resolve(ctx, users[i].Name)
		if err != nil {
			inv.Warnings = append(inv.Warnings, fmt.Sprintf("could not resolve groups for %q: %v", users[i].Name, err))
			continue
		}
		users[i].GID = creds.GID
		for _, gid := range creds.Groups {
			name, ok := byGID[gid]
			if !ok || gid == creds.UID {
				continue // unnamed, or the user's own private group
			}
			users[i].Groups = append(users[i].Groups, name)
			members[gid] = append(members[gid], users[i].Name)
		}
		sort.Strings(users[i].Groups)
	}
	for i := range groups {
		groups[i].Members = members[groups[i].GID]
		sort.Strings(groups[i].Members)
		if groups[i].Members == nil {
			groups[i].Members = []string{}
		}
	}

	// SMB state. A failure here is reported, not fatal: the unix side of the
	// picture is still worth showing, and pdbedit being absent is exactly what
	// happens in a deployment that has not finished coming up.
	smb, err := a.smbAccounts(ctx)
	if err != nil {
		inv.Warnings = append(inv.Warnings, fmt.Sprintf("SMB account state unavailable: %v", err))
	} else {
		for i := range users {
			if acct, ok := smb[users[i].Name]; ok {
				users[i].SMB = &SMBAccount{Enabled: acct}
			} else {
				users[i].SMB = nil
			}
		}
	}

	// Usage is served from the cache and never computed inline: on a real
	// dataset the answer takes long enough that computing it per request would
	// make the page time out under exactly the conditions an operator opens it.
	for i := range users {
		if n, ok := a.usage.get(users[i].UID); ok {
			v := n
			users[i].UsageBytes = &v
		}
	}

	inv.Users, inv.Groups = users, groups
	return inv, nil
}

// Column positions in `usersync export --format csv`. They are the RFC2307
// attributes a directory would be seeded with:
//
//	type,name,uid_number,gid_number,unix_home_directory,login_shell
//
// A GROUP row leaves uid_number EMPTY and carries its number in gid_number:
//
//	group,team-a,,10001,,
//	user,alice,3001,3001,/srv/data/homes/alice,/usr/sbin/nologin
//
// Reading column 2 for both is why group rows used to be dropped whole, which
// left nothing to translate a user's gids into names and made every team column
// on the operator page empty.
const (
	colType = 0
	colName = 1
	colUID  = 2
	colGID  = 3
	colHome = 4
)

// parseExportCSV reads `usersync export --format csv`.
func parseExportCSV(r io.Reader) ([]User, []Group, error) {
	rd := csv.NewReader(r)
	rd.FieldsPerRecord = -1
	rows, err := rd.ReadAll()
	if err != nil {
		return nil, nil, fmt.Errorf("admin: parse account export: %w", err)
	}

	num := func(row []string, col int) (uint32, bool) {
		if col >= len(row) {
			return 0, false
		}
		v, err := strconv.ParseUint(row[col], 10, 32)
		return uint32(v), err == nil
	}

	users, groups := []User{}, []Group{}
	for i, row := range rows {
		if i == 0 || len(row) <= colGID {
			continue // header, or a row shorter than the fields read below
		}
		switch row[colType] {
		case "user":
			uid, ok := num(row, colUID)
			if !ok {
				continue
			}
			u := User{Name: row[colName], UID: uid, Groups: []string{}}
			u.GID, _ = num(row, colGID)
			if len(row) > colHome {
				u.Home = row[colHome]
			}
			users = append(users, u)
		case "group":
			gid, ok := num(row, colGID)
			if !ok {
				continue
			}
			groups = append(groups, Group{Name: row[colName], GID: gid, Members: []string{}})
		}
	}
	sort.Slice(users, func(i, j int) bool { return users[i].Name < users[j].Name })
	sort.Slice(groups, func(i, j int) bool { return groups[i].Name < groups[j].Name })
	return users, groups, nil
}

// smbAccounts reports which names have a tdbsam account and whether it is
// enabled.
//
// The parsing lives in internal/auth because the sign-in gate asks the same
// question of the same output, and two readers of `pdbedit -L -v` that drift
// apart would disagree about who is suspended — this page saying one thing and
// the login saying another.
func (a *Admin) smbAccounts(ctx context.Context) (map[string]bool, error) {
	out, err := a.cfg.Runner.Run(ctx, "", a.cfg.PdbeditBin, "-L", "-v")
	if err != nil {
		return nil, err
	}
	return auth.ParseAccountFlags(out), nil
}

// Drift is one disagreement between the roster and the system.
type Drift struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	Code string `json:"code"`
	Want uint32 `json:"want,omitempty"`
	Got  uint32 `json:"got,omitempty"`
}

// DriftReport is what `usersync audit` found.
type DriftReport struct {
	Findings []Drift `json:"findings"`
	// OK is true when the roster and the system agree. It is reported
	// separately from an empty Findings list so a failed audit is never
	// indistinguishable from a clean one.
	OK bool `json:"ok"`

	// Available is false when usersync is not installed at all.
	//
	// That is not a failure, it is a property of the deployment: the local
	// stack provisions accounts from a shell script and ships no usersync, so
	// there is no roster to compare against and nothing has gone wrong. Folding
	// it into Error made the page show a broken exec message for a check that
	// simply does not apply here.
	Available bool `json:"available"`

	// Error is set only when usersync EXISTS and could not answer.
	Error string `json:"error,omitempty"`
}

// Audit asks usersync whether the roster and the system still agree.
//
// This is the check that survives a handover to a directory service: once
// `mode: audit` is set, nothing else verifies that the numbers a name resolves
// to are the numbers the roster reserved for it.
//
// A non-zero exit is the normal way audit reports disagreement, so it is parsed
// rather than treated as a failure.
func (a *Admin) Audit(ctx context.Context) (*DriftReport, error) {
	out, runErr := a.cfg.Runner.Run(ctx, "", a.cfg.UsersyncBin, "audit", "--json")
	if notInstalled(runErr) {
		return &DriftReport{Findings: []Drift{}, Available: false}, nil
	}

	var payload struct {
		Findings []Drift `json:"findings"`
	}
	// audit logs to stderr and prints its report to stdout, so the JSON may not
	// start at byte zero.
	start := strings.IndexByte(out, '{')
	if start >= 0 {
		if err := json.Unmarshal([]byte(out[start:]), &payload); err == nil {
			return &DriftReport{
				Findings:  nonNil(payload.Findings),
				OK:        len(payload.Findings) == 0,
				Available: true,
			}, nil
		}
	}
	if runErr != nil {
		return &DriftReport{Findings: []Drift{}, Available: true, Error: runErr.Error()}, nil
	}
	return nil, fmt.Errorf("admin: could not read the audit report")
}

// notInstalled reports whether a command failed because the binary is absent,
// as opposed to running and returning something.
//
// run.Exec wraps the underlying error with %w, and os/exec reports a failed
// PATH lookup as an *exec.Error wrapping exec.ErrNotFound, so the distinction
// survives to here rather than having to be recovered from message text.
func notInstalled(err error) bool {
	return err != nil && errors.Is(err, exec.ErrNotFound)
}

func nonNil(d []Drift) []Drift {
	if d == nil {
		return []Drift{}
	}
	return d
}

// enumerate lists accounts straight from the name service.
//
// This is the fallback, and it is a worse question than the one usersync
// answers: `getent passwd` with no argument asks each NSS module to list
// everything it has, and winbind declines by default. So a domain-served
// account is simply absent — which is why the caller attaches a warning saying
// so rather than presenting the result as the whole picture.
//
// The id windows are the reserved band (nas-design.md ADR-8): uid 3000-9999 for
// people, gid 10000-19999 for teams. Filtering by them is what keeps system
// accounts and the operator group itself out of a page about researchers.
func (a *Admin) enumerate(ctx context.Context) ([]User, []Group) {
	users, groups := []User{}, []Group{}

	if out, err := a.cfg.Runner.Run(ctx, "", "getent", "passwd"); err == nil {
		for _, line := range strings.Split(out, "\n") {
			// name:x:uid:gid:gecos:home:shell — GECOS may contain a colon, so
			// home and shell are counted from the END.
			f := strings.Split(line, ":")
			if len(f) < 7 {
				continue
			}
			uid, err := strconv.ParseUint(f[2], 10, 32)
			if err != nil || uid < minManagedUID || uid > maxManagedUID {
				continue
			}
			gid, _ := strconv.ParseUint(f[3], 10, 32)
			users = append(users, User{
				Name:     f[0],
				UID:      uint32(uid),
				GID:      uint32(gid),
				FullName: f[4],
				Home:     f[len(f)-2],
				Groups:   []string{},
			})
		}
	}

	if out, err := a.cfg.Runner.Run(ctx, "", "getent", "group"); err == nil {
		for _, line := range strings.Split(out, "\n") {
			f := strings.Split(line, ":")
			if len(f) < 3 {
				continue
			}
			gid, err := strconv.ParseUint(f[2], 10, 32)
			if err != nil || gid < minManagedGID || gid > maxManagedGID {
				continue
			}
			groups = append(groups, Group{Name: f[0], GID: uint32(gid), Members: []string{}})
		}
	}

	sort.Slice(users, func(i, j int) bool { return users[i].Name < users[j].Name })
	sort.Slice(groups, func(i, j int) bool { return groups[i].Name < groups[j].Name })
	return users, groups
}

// The band reserved for this deployment. It is duplicated from usersync's
// defaults rather than read from its config, because this path exists exactly
// for the case where usersync cannot be asked anything.
const (
	minManagedUID = 3000
	maxManagedUID = 9999
	minManagedGID = 10000
	maxManagedGID = 19999
)
