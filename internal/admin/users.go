package admin

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
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

// Inventory is the account picture the admin page renders.
type Inventory struct {
	Users  []User  `json:"users"`
	Groups []Group `json:"groups"`

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
		users, groups = a.enumerate(ctx)
		inv.Warnings = append(inv.Warnings, fmt.Sprintf(
			"usersync could not be asked (%v); listing from the name service instead. "+
				"Accounts served by a directory may be missing, because winbind does not enumerate them by default.", err))
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

// parseExportCSV reads `usersync export --format csv`.
//
// Its columns are the RFC2307 attributes a directory would be seeded with:
// type,name,uid_number,gid_number,unix_home_directory,login_shell — with `type`
// distinguishing a user row from a group row.
func parseExportCSV(r io.Reader) ([]User, []Group, error) {
	rd := csv.NewReader(r)
	rd.FieldsPerRecord = -1
	rows, err := rd.ReadAll()
	if err != nil {
		return nil, nil, fmt.Errorf("admin: parse account export: %w", err)
	}

	users, groups := []User{}, []Group{}
	for i, row := range rows {
		if i == 0 || len(row) < 4 {
			continue // header, or a row shorter than the fields we read
		}
		id, err := strconv.ParseUint(row[2], 10, 32)
		if err != nil {
			continue
		}
		switch row[0] {
		case "user":
			u := User{Name: row[1], UID: uint32(id), Groups: []string{}}
			if len(row) > 4 {
				u.Home = row[4]
			}
			users = append(users, u)
		case "group":
			groups = append(groups, Group{Name: row[1], GID: uint32(id), Members: []string{}})
		}
	}
	sort.Slice(users, func(i, j int) bool { return users[i].Name < users[j].Name })
	sort.Slice(groups, func(i, j int) bool { return groups[i].Name < groups[j].Name })
	return users, groups, nil
}

// smbAccounts reports which names have a tdbsam account and whether it is
// enabled. `pdbedit -L -v` prints an "Account Flags" field where D means
// disabled — the same bit `smbpasswd -d` sets.
func (a *Admin) smbAccounts(ctx context.Context) (map[string]bool, error) {
	out, err := a.cfg.Runner.Run(ctx, "", a.cfg.PdbeditBin, "-L", "-v")
	if err != nil {
		return nil, err
	}
	return parsePdbedit(out), nil
}

func parsePdbedit(out string) map[string]bool {
	accounts := map[string]bool{}
	current := ""
	for _, line := range strings.Split(out, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		switch key {
		case "Unix username":
			current = value
			// Present until the flags say otherwise; a record with no flags line
			// is still an account.
			accounts[current] = true
		case "Account Flags":
			if current != "" {
				accounts[current] = !strings.Contains(value, "D")
			}
		}
	}
	return accounts
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
	OK    bool   `json:"ok"`
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

	var payload struct {
		Findings []Drift `json:"findings"`
	}
	start := strings.IndexByte(out, '{')
	if start >= 0 {
		if err := json.Unmarshal([]byte(out[start:]), &payload); err == nil {
			return &DriftReport{
				Findings: nonNil(payload.Findings),
				OK:       len(payload.Findings) == 0,
			}, nil
		}
	}
	if runErr != nil {
		return &DriftReport{Findings: []Drift{}, OK: false, Error: runErr.Error()}, nil
	}
	return nil, fmt.Errorf("admin: could not read the audit report")
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
