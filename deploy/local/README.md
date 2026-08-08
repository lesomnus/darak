# 로컬 테스트 스택

```sh
docker compose -f deploy/local/docker-compose.yaml up --build
```

- 웹 <http://localhost:8080> — `alice` / `darak`
- SMB `//localhost:1445/alice`, `//localhost:1445/team-a`

계정은 [`config/roster.yaml`](./config/roster.yaml)에서 나옵니다. 초기 비밀번호는 전부 `darak`이고, 바꾸면 그 비밀번호가 유지됩니다.

---

## 무엇이 유지되고, 무엇이 유지되지 않는가

| | |
| --- | --- |
| `data` 볼륨 → `/srv/data` | 파일 그 자체 |
| `samba` 볼륨 → `/var/lib/samba` | tdbsam. **바꾼 비밀번호**가 여기 있습니다 |
| `state` 볼륨 → `/var/lib/darak` | 공유 링크 토큰 |
| **`/etc/passwd`** | **유지되지 않습니다.** 매 부팅마다 재구성됩니다 |

마지막 줄이 이 스택에서 유일하게 설명이 필요한 부분입니다.

### 계정을 볼륨에 넣지 않는 이유

두 가지입니다.

**첫째, 되지 않습니다.** `useradd`는 `/etc/passwd`를 임시 파일에 쓰고 `rename`으로 교체합니다. 파일 하나를 bind mount 하면 그 마운트는 inode에 묶여 있으므로, rename 하는 순간 마운트가 가리키는 것과 실제 파일이 갈라집니다. 디렉터리째 `/etc`를 볼륨으로 올리는 건 나머지 전부를 망가뜨립니다.

**둘째, 필요하지 않습니다.** 볼륨 안의 데이터는 소유자를 **이름이 아니라 번호로** 알고 있습니다. `/etc/passwd`는 그 번호에 이름을 붙이는 파생 상태일 뿐입니다. 그래서 `config/roster.yaml`가 uid/gid를 **고정**하고, 부팅마다 계정을 그 번호로 다시 만들면 파일은 계속 같은 사람의 것입니다.

이건 임시방편이 아니라 [nas-design.md](../../nas-design.md)의 ADR-8이 온프렘 AD 전환에서 기대는 것과 **정확히 같은 성질**입니다. 살아남아야 하는 것은 번호이고, 그 번호를 이름과 잇는 기록은 언제든 다시 만들 수 있습니다. 이 스택은 그걸 시작할 때마다 한 번씩 실증합니다.

직접 확인하려면:

```sh
docker compose -f deploy/local/docker-compose.yaml down   # 컨테이너를 통째로 파괴
docker compose -f deploy/local/docker-compose.yaml up -d
docker compose -f deploy/local/docker-compose.yaml exec darak \
  stat -c '%n %U:%G %a' /srv/data/homes/alice/diary.txt
```

`/etc/passwd`가 사라졌다 새로 만들어졌는데도 `alice:alice 600` 그대로입니다.

### 완전히 초기화하려면

```sh
docker compose -f deploy/local/docker-compose.yaml down -v
```

---

## 계정 추가

[`config/roster.yaml`](./config/roster.yaml)를 편집하고 다시 올리면 됩니다.

```
group team-c 10003
user erin 3005 team-a,team-c
```

uid/gid는 예약 대역 안이어야 합니다 — 사용자 `3000`–`9999`, 팀 그룹 `10000`–`19999`. **이미 쓴 번호를 재사용하지 마세요.** 볼륨에 그 번호로 된 파일이 남아 있으면 새 사람이 그걸 물려받습니다. 이 스택에서는 그저 헷갈리는 정도지만, 실서버에서는 그게 사고입니다.

> 실환경에서는 이 파일이 `roster.yaml`이고 [usersync](https://github.com/lesomnus/usersync)가 적용합니다. usersync는 은퇴한 사용자를 `status: reserved` 무덤돌로 남겨 번호 재사용을 아예 막습니다. 여기서는 두 번째 저장소를 요구하지 않으려고 작은 스크립트가 그 자리를 대신합니다 — 둘이 어긋나면 usersync가 맞습니다.

## 확인해볼 만한 것

**같은 파일, 같은 규칙.** SMB로 올린 파일과 웹으로 올린 파일이 구분되지 않아야 합니다.

```sh
docker compose -f deploy/local/docker-compose.yaml exec darak sh -c \
  "echo hi > /tmp/x && smbclient //127.0.0.1/team-a -U bob%darak -c 'put /tmp/x viasmb.txt'"
docker compose -f deploy/local/docker-compose.yaml exec darak \
  stat -c '%n %U:%G %a' /srv/data/teams/team-a/viasmb.txt
# bob:team-a 660 — 웹으로 올린 것과 동일
```

**권한은 커널이 정한다.** `bob`으로 로그인해 `alice`의 홈을 열어보면 403입니다. 애플리케이션이 그렇게 정해서가 아니라, 요청이 `bob`으로 실행되기 때문입니다.

**덮어써도 잃지 않는다.** 팀 파일을 두 번 올린 뒤 `teams/team-a/.trash/`를 보세요. 이전 판이 거기 있습니다.

## 인터페이스 개발

컨테이너를 다시 빌드하지 않고 UI만 고치려면:

```sh
cd web && npm run dev     # :5173, /api 와 /s 를 :8080 으로 프록시
```

## 이 설정을 실서버에 쓰지 마세요

**메커니즘은 그대로 실서버에서 씁니다** — 계정을 컨테이너 안에서 roster로부터 재구성하는 방식은 [ADR-9](../../nas-design.md)로 채택됐고, [`deploy/prod/`](../prod/)가 같은 구조입니다. 다른 것은 설정뿐입니다:

| | 여기 | `deploy/prod/` |
|---|---|---|
| 계정 관리 | 대역 스크립트 | **usersync**(무덤돌, 범위 가드, 감사) |
| TLS | 없음, `-secure-cookies=false` | 필수 — 없으면 기동 거부 |
| 비밀번호 | compose에 평문 | 시드 파생, 볼륨에 보관 |
| 데이터 | docker 볼륨 | 실제 데이터셋(bind) |
| SMB | 1445로 매핑 | 호스트 네트워킹, 445 |

## 운영 페이지

`docker-compose.yaml`의 `DARAK_ADMIN_MEMBERS`에 적힌 사용자만 **관리** 버튼을 봅니다(기본 alice).
실서버와 **같은 환경변수**입니다.

`admin`은 roster의 그룹이 아닙니다. roster에 그룹을 선언하면 팀 폴더와 SMB 공유가 따라오는데,
계정을 관리할 수 있다는 것이 공유 디렉터리를 만들 이유는 아니므로 엔트리포인트가 gid 2000
(관리 대역 10000–19999 아래)에 따로 만듭니다.

alice로 로그인하면 사용자 목록·디스크·사용자별 사용량·선언 대조가 보이고, 다른 사용자의 SMB
계정을 잠그거나 비밀번호를 재설정할 수 있습니다. bob으로 로그인하면 버튼이 없고, API를 직접
부르면 404입니다.

## 계정은 usersync가 만든다

로컬 스택도 실서버와 **같은 도구, 같은 파일, 같은 순서**로 계정을 만듭니다. 예전에는 셸 스크립트가
`users.conf`를 읽었는데, 그러면 여기서 검증되는 계정 경로가 실서버에서 도는 경로와 달랐습니다.

부팅 순서(`deploy/prod/entrypoint.sh`와 동일):

```
layout 루트 생성 → smb.conf 전역 섹션 seed → usersync validate → plan → apply
  → operator 그룹 → usersync shares --write → smbd/winbindd → darak
```

순서가 보이는 것보다 까다롭습니다. **layout이 먼저**인 이유는 usersync가 홈을 `MkdirAll`로 만드는데
중간 디렉터리가 *잎의* 모드를 받기 때문입니다 — `/srv/data/homes`가 없으면 0700 root 소유로 생겨서
아무도 자기 홈에 들어가지 못합니다. **smb.conf가 계정보다 먼저**인 이유는 `usersync apply`가
`smbpasswd`로 SMB 계정을 등록하는데 smbpasswd는 읽을 수 있는 설정 없이는 실행을 거부하기 때문입니다.

초기 비밀번호는 **시드에서 파생**되며(시드는 첫 기동에 state 볼륨에 생성됩니다) 컨테이너 로그의
배너에 사용자별로 출력됩니다. 고정 비밀번호 하나를 쓰지 않는 이유는 그것이 실서버에 없는 메커니즘이기
때문입니다. 사용자가 바꾼 비밀번호는 재시작해도 되돌아가지 않습니다(`/var/lib/samba`가 볼륨).

`config/usersync.yaml`의 `mode`를 `audit`으로 바꾸면 AD 인계 이후를 리허설할 수 있습니다.
`apply`/`purge`가 거부되고 엔트리포인트는 `usersync audit`으로 검증만 합니다.

roster에는 `status: disabled`(erin)와 `status: reserved`(frank)가 하나씩 들어 있습니다.
전자는 SMB만 잠기고 홈과 uid가 남으며, 후자는 계정이 없고 uid 3006만 예약됩니다 — 둘 다
users.conf 시절에는 표현할 수 없어 검증되지 않던 상태입니다.
