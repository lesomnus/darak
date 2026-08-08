# 로컬 테스트 스택

```sh
docker compose -f deploy/local/docker-compose.yaml up --build
```

- 웹 <http://localhost:8080> — `alice` / `darak`
- SMB `//localhost:1445/alice`, `//localhost:1445/team-a`

계정은 [`users.conf`](./users.conf)에서 나옵니다. 초기 비밀번호는 전부 `darak`이고, 바꾸면 그 비밀번호가 유지됩니다.

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

**둘째, 필요하지 않습니다.** 볼륨 안의 데이터는 소유자를 **이름이 아니라 번호로** 알고 있습니다. `/etc/passwd`는 그 번호에 이름을 붙이는 파생 상태일 뿐입니다. 그래서 `users.conf`가 uid/gid를 **고정**하고, 부팅마다 계정을 그 번호로 다시 만들면 파일은 계속 같은 사람의 것입니다.

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

[`users.conf`](./users.conf)를 편집하고 다시 올리면 됩니다.

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

## 주의

이건 **테스트용**입니다. 실서버에 쓰지 마세요.

- 비밀번호가 평문으로 compose 파일에 있습니다
- `-secure-cookies=false` — 평문 HTTP 전제입니다
- 계정 관리가 usersync가 아니라 스크립트입니다(무덤돌도, 감사도 없습니다)
- SMB가 인증 없는 로컬 네트워크에 445를 엽니다
