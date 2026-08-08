# 배포

```sh
cd deploy/prod
cp .env.example .env && $EDITOR .env
mkdir -p config secrets tls
# config/roster.yaml, config/usersync.yaml, secrets/seed.secret, tls/*.pem
docker compose up -d --build
```

전체 근거는 [nas-design.md ADR-9](../../nas-design.md)에 있습니다. 여기서는 운영에 필요한 것만 적습니다.

---

## usersync 버전 고정

usersync는 아직 태그된 릴리즈가 없어서 **커밋 해시**로 고정합니다:

```
USERSYNC_VERSION=b0fe5b8da659bc0bd542289e4f8dfabfafcbb231
```

`lesomnus/usersync`의 `main` HEAD입니다. 올릴 때는 그 저장소에서 `git rev-parse HEAD`를
읽어 `.env`에 넣으세요. 릴리즈 태그가 생기면 태그로 바꾸는 편이 낫습니다 — 해시는
브랜치가 움직여도 유효하지만, 무엇이 들어있는지는 말해주지 않습니다.

---

## 무엇이 유지되고, 무엇이 유지되지 않는가

| | |
|---|---|
| `DARAK_DATA` → `/srv/data` | 파일. 실제 데이터셋(bind), docker 볼륨이 아닙니다 |
| `samba` 볼륨 → `/var/lib/samba` | tdbsam. **비밀번호는 roster에서 재생성할 수 없습니다** |
| `state` 볼륨 → `/var/lib/darak` | 공유 링크 토큰 |
| `DARAK_CONFIG` → `/etc/darak` (ro) | roster.yaml, usersync.yaml — **입력**이지 상태가 아닙니다 |
| **`/etc/passwd`** | **유지되지 않습니다.** 부팅마다 roster에서 재구성됩니다 |

마지막이 이 배포의 핵심이고, 설명은 [ADR-9](../../nas-design.md)에 있습니다. 요점만 말하면: 데이터는 소유자를 **번호로** 알고, roster가 그 번호를 고정하므로, 이름을 붙이는 기록은 언제든 다시 만들 수 있습니다.

직접 확인하려면 컨테이너를 통째로 지우고 다시 올린 뒤 아무 파일이나 `stat` 해보세요. 소유자가 그대로입니다.

## 운영 페이지

`admin` POSIX 그룹에 속한 사용자만 웹에서 **관리** 버튼을 보고 `/api/admin/*`를 쓸 수 있습니다.
`.env`에서 지정합니다:

```
DARAK_ADMIN_MEMBERS=skim,jlee
```

- **비워두면 아무도 못 씁니다.** 그게 기본값입니다.
- 이름은 `roster.yaml`에 선언된 계정이어야 합니다. 그룹은 usersync가 각 사용자의 보조 그룹을
  세팅한 **뒤에** 적용되므로 재조정에 지워지지 않습니다.
- 멤버십은 **요청마다** 다시 확인합니다. `gpasswd -d`로 빼면 즉시 잃습니다 — 세션이 12시간이라
  로그인 시점에 판정해 두면 권한을 뺏은 뒤에도 하루가 남기 때문입니다.
- `DARAK_ADMIN_GID`(기본 2000)는 **첫 기동 전에만** 바꾸세요. 이 gid는 파일에 남지는 않지만,
  번호가 움직이면 그 안에 있던 사람들을 더 이상 가리키지 않습니다.

| | |
|---|---|
| 조회 | 사용자·팀, uid/gid, SMB 상태, 디스크 용량, 사용자별 사용량, `usersync audit` 결과 |
| 변경 | SMB 계정 잠금/해제, SMB 비밀번호 재설정 (둘 다 tdbsam만), **모든 팀의 구성원** |
| **불가** | 계정 생성·삭제, uid 변경, `status` 변경, 소유자 지정 — `roster.yaml`을 고쳐 커밋하고 재시작하세요 |

## 팀 소유자

관리자와는 **다른 축**입니다. roster에서 팀별로 지정하고, 그 사람은 **자기 팀의 구성원 추가·제외만**
할 수 있습니다. 운영 페이지의 나머지는 못 봅니다.

```yaml
groups:
  - name: team-a
    gid: 10001
    owners: [skim]      # POSIX: /etc/gshadow 관리자 필드에 반영됨
```

관리자라고 자동으로 소유자가 되지는 않습니다. 어떤 팀을 실제로 운영하는 관리자는 다른 사람과
똑같이 `owners`에 적으세요 — 그러지 않으면 "이 팀은 누가 책임지나"에 답할 수 없습니다.

소유자가 웹에서 구성원을 바꾸면 `usersync member`가 `roster.yaml`의 해당 **한 줄만** 고치고
(주석·서식 보존, 쓰기 전 검증, 파일 잠금), 이어서 `usersync apply`가 동기로 돌아 바로 반영됩니다.
근거는 [ADR-10](../../nas-design.md).

사용자별 사용량은 백그라운드에서 잽니다(`-usage-interval`, 기본 30분). ZFS면 `zfs userspace`가
즉답하고, 아니면 트리를 한 번 순회합니다. 페이지는 마지막 측정값을 잰 시각과 함께 보여줍니다.

---

## 반드시 확인할 것

**userns-remap이 꺼져 있어야 합니다.** 켜져 있으면 컨테이너의 uid 3001이 디스크에서는 다른 번호가 되고, 이 서버가 쓰는 모든 파일이 roster가 이름 붙이지 않은 번호의 소유가 됩니다. **아무것도 그 시점에 실패하지 않습니다** — 몇 달 뒤 "내 파일이 안 열린다"로 나타납니다.

darak이 기동 시 `/proc/self/uid_map`을 읽어 **거부**하므로 사고로 이 상태가 되지는 않습니다. 확인하려면:

```sh
grep userns-remap /etc/docker/daemon.json    # 없어야 정상
docker compose exec darak cat /proc/self/uid_map   # "0 0 4294967295"
```

**TLS가 필요합니다.** 세션 쿠키가 `Secure`라서 평문 HTTP에서는 브라우저가 되돌려 보내지 않습니다 — 로그인이 성공한 것처럼 보이고 아무 일도 일어나지 않습니다. `DARAK_TLS_CERT`/`DARAK_TLS_KEY`를 주거나, 앞단이 종단한다면 `DARAK_BEHIND_PROXY=1`을 명시하세요. 둘 다 없으면 기동을 거부합니다.

**시드 파일은 호스트에서 `0600`으로 두세요.** 읽기 전용으로 마운트되므로 컨테이너가 고칠 수 없고, usersync가 기동할 때마다 경고합니다.

```sh
chmod 600 secrets/seed.secret
```

## 계정 관리

`config/roster.yaml`을 편집하고 재시작하면 됩니다. 부팅 순서는 `usersync validate` → `plan` → `apply`이고, roster에 문제가 있으면 **아무것도 바꾸지 않고 기동을 중단**합니다.

재시작 없이 적용하려면:

```sh
docker compose exec darak sh -c 'cd /etc/darak && usersync plan'
docker compose exec darak sh -c 'cd /etc/darak && usersync apply && usersync shares --write && smbcontrol smbd reload-config'
```

> `/etc/darak`는 **쓰기 가능**합니다. darak이 팀 소유자를 대신해 `users[].groups`만 편집하기
> 때문입니다(`usersync member` 경유, 쓰기 전 검증, 주석·서식 보존). 계정 생성·uid·`status`는
> 여전히 손편집이고, 그 디렉터리는 계속 버전관리하에 두세요 — 강제되지는 않지만 이력이 곧 기록입니다.

**은퇴자는 지우지 말고 `status: disabled` 또는 `reserved`로 남기세요.** 지우면 uid 예약이 풀리고, 볼륨에는 그 번호로 된 파일이 남아 있습니다. 여기서는 그게 사고입니다.

## 백업

`DARAK_DATA`와 `samba` 볼륨 둘 다입니다. 데이터만 백업하면 복구 후 아무도 로그인할 수 없습니다.

호스트에서 백업할 때는 **이름이 아니라 번호로** 복사해야 합니다 — 호스트에는 그 계정들이 없습니다:

```sh
rsync -aHAX --numeric-ids /srv/data/ /backup/darak/
```

`--numeric-ids`가 없으면 rsync가 이름으로 매핑하려 하고, 호스트에 없는 이름은 조용히 다른 소유자가 됩니다.

**휴지통은 백업이 아닙니다.** 랜섬웨어는 휴지통도 지웁니다.

## AD 전환 시

`secrets.tdb`가 `/var/lib/samba`에 들어가므로 볼륨은 그대로 두면 됩니다. `net ads join`은 컨테이너 안에서 하고, 조인 상태는 그 볼륨에 남습니다. 절차는 usersync의 `identity-roadmap.md`.

## 이 구성이 하지 않는 것

- **감사 로그** — 아직 없습니다(Phase 2)
- **정리 cron** — 30일 경과 휴지통, 고아 임시 파일 정리가 아직 없습니다
- **썸네일** — 없습니다. 있게 되면 비특권 seccomp 워커로 분리해야 합니다
- **SMB 휴지통·감사** — [의도적으로](../../nas-design.md) 제공하지 않습니다
