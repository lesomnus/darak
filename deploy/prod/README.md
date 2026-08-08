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
USERSYNC_VERSION=2e96cb143269436bc28e901be016dd8ebe50db92
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

> `/etc/darak`는 읽기 전용으로 마운트됩니다. roster는 버전관리되는 곳에서 고치고, 컨테이너가 편집하지 않습니다.

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
