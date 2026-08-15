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
USERSYNC_VERSION=33478f66b851a588a8ddf157c72d6206cf983705
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
| `state` 볼륨 → `/var/lib/darak` | 공유 링크 토큰, SSO 매핑·요청 큐·변경 이력 |
| `DARAK_CONFIG` → `/etc/darak` (ro) | roster.yaml, usersync.yaml — **입력**이지 상태가 아닙니다 |
| **`/etc/passwd`** | **유지되지 않습니다.** 부팅마다 roster에서 재구성됩니다 |

마지막이 이 배포의 핵심이고, 설명은 [ADR-9](../../nas-design.md)에 있습니다. 요점만 말하면: 데이터는 소유자를 **번호로** 알고, roster가 그 번호를 고정하므로, 이름을 붙이는 기록은 언제든 다시 만들 수 있습니다.

직접 확인하려면 컨테이너를 통째로 지우고 다시 올린 뒤 아무 파일이나 `stat` 해보세요. 소유자가 그대로입니다.

## 이름과 로고

상단 바 왼쪽과 로그인 화면, 그리고 브라우저 탭 제목에 들어갑니다. 둘 다 선택이고, 아무것도 주지 않으면 기본 글리프에 `파일 서버`입니다.

```
DARAK_BRAND_NAME=다락 연구소
DARAK_BRAND_LOGO=/etc/darak/logo.svg
```

- 로고 파일은 **컨테이너가 볼 수 있는 경로**여야 합니다. `DARAK_CONFIG`가 `/etc/darak`로 마운트되므로 거기에 두고 그 경로로 부르는 것이 가장 간단합니다.
- 확장자로 형식을 정합니다: `.svg .png .jpg .jpeg .webp .gif .avif .ico`. 다른 것은 **기동 실패**입니다. 없는 파일, 빈 파일, 1 MiB 초과도 마찬가지입니다 — 구석에 깨진 이미지가 뜬 채로 일주일이 지나는 것보다 낫습니다.
- **시작할 때 한 번 읽어 메모리에 둡니다.** 파일을 바꿨으면 재시작해야 반영됩니다.
- **테마를 따라가지 않습니다.** `<img>`로 그리므로 페이지의 CSS 바깥에 있고, `currentColor`도 `data-theme`도 보지 못합니다. 밝은 배경과 어두운 배경 양쪽에서 읽히는 색을 쓰거나, 로고 안에서 `@media (prefers-color-scheme: dark)`를 직접 처리하는 SVG를 쓰세요(이 경우 페이지의 테마 스위치가 아니라 **OS 설정**을 따라갑니다).
- 이름은 로고 옆에 **HTML 텍스트**로 그려집니다. 좁은 화면에서는 숨고, 길면 줄임표가 붙습니다. 그래서 글자가 박힌 이미지보다 글리프 + `DARAK_BRAND_NAME` 쪽이 대개 낫습니다.

두 라우트 다 **세션이 없습니다**(`GET /api/branding`, `GET /api/branding/logo`). 로그인하기 전 화면에 걸리는 것이라 그럴 수밖에 없고, 노출되는 것은 조직 이름과 그림 하나입니다.

## 회사 계정으로 로그인 (SSO)

선택입니다. `DARAK_OIDC_ISSUER`가 비어 있으면 버튼도 없고 `/api/sso/*`는 전부 404이며, 지금까지처럼 모두 비밀번호로 들어옵니다.

```
DARAK_OIDC_ISSUER=https://login.microsoftonline.com/<tenant-id>/v2.0
DARAK_OIDC_CLIENT_ID=...
DARAK_OIDC_REDIRECT_URL=https://darak.example.com/api/sso/callback
DARAK_OIDC_CLIENT_SECRET_FILE=/run/secrets/oidc_client_secret
```

### 켜도 바뀌지 않는 것

**퇴사 처리는 여전히 `roster.yaml` 한 줄입니다.** IdP는 "이 사람이 누구인가"만 답하고, 그 계정이 지금 로그인해도 되는지는 **매 로그인마다 tdbsam에** 묻습니다. `status: disabled` → `usersync apply` → tdbsam `D` 플래그 → SMB·비밀번호·SSO가 동시에 닫힙니다. IdP를 잠글 필요도 없고, IdP만 잠가서 절반만 닫히는 상태도 만들 수 없습니다.

**비밀번호 경로는 그대로 있습니다.** SSO는 추가 경로입니다. IdP 계정이 없는 사람도, IdP가 죽어 있는 동안에도 들어올 수 있습니다.

**SMB는 아무것도 바뀌지 않습니다.** SMB 인증에는 OIDC 토큰이 들어갈 자리가 없습니다. 그래서 **이 기능은 서버를 인터넷에 노출해도 된다는 뜻이 아닙니다** — SMB 경로는 여전히 비밀번호 하나입니다.

### 등록 흐름

1. 아직 등록되지 않은 사람이 SSO 버튼을 누릅니다 → **로그인되지 않고**, 요청만 기록됩니다. 화면에는 인식된 주소와 "비밀번호로는 지금도 들어올 수 있다"가 표시됩니다
2. 관리자가 운영 페이지의 **회사 계정(SSO)** 패널에서 그 요청에 계정을 지정합니다
3. 그 사람이 다시 SSO로 들어오면 로그인됩니다. 이때 IdP의 불변 식별자(`sub`)가 고정되고, **이후로는 주소가 아니라 그 식별자가 인증합니다**

주소를 미리 받아 적는 절차가 없는 것이 요점입니다. IdP가 실제로 무엇을 주장하는지(어느 클레임, 어떤 대소문자, 여러 주소 중 무엇)는 미리 알 수 없고, 어긋나면 **비밀번호를 틀린 것과 구별되지 않는 401**이 됩니다.

`sub` 고정이 막는 것은 하나 더 있습니다 — 퇴사자의 주소가 나중에 신입에게 재할당되는 경우. 고정된 것과 다른 `sub`가 같은 주소를 들고 오면 거부되고 로그에 남습니다.

### 테넌트를 반드시 고정하세요

발급자 URL에 테넌트 id가 들어 있으면(`.../<tenant-id>/v2.0`) `iss` 검사가 이미 고정합니다. **`/common`, `/organizations`, `/consumers`를 쓴다면 `DARAK_OIDC_TENANT`가 필수이고, 없으면 기동을 거부합니다.** 그 발급자들은 **아무 디렉터리의 토큰이나 정상 검증**하며, 각 테넌트가 자기 사용자에게 붙인 주소를 그대로 주장합니다 — 테넌트 검사가 없으면 주소를 식별자로 쓰는 것 자체가 무의미합니다.

### 시크릿

`DARAK_OIDC_CLIENT_SECRET_FILE`로 경로를 주거나, 값만 있다면 `DARAK_OIDC_CLIENT_SECRET`에 넣으면 entrypoint가 상태 디렉터리에 0600으로 쓰고 변수를 지웁니다.

**명령줄 인자로는 절대 들어가지 않고, 환경변수로 남지도 않습니다.** argv는 `/proc`으로 누구나 읽고, 헬퍼 프로세스는 서버에서 exec되어 환경을 물려받으므로 — 환경에 남겨두면 이 서버가 권한으로 갈라놓으려는 바로 그 사용자들이 자기 헬퍼의 `/proc/self/environ`으로 읽을 수 있습니다.

### 리버스 프록시가 로그인시키기 (forward-auth)

조직에 이미 SSO 프록시(예: Traefik `ForwardAuth` + oauth2-proxy)가 있다면, darak가 자체 코드 플로우를 돌리는 대신 **그 프록시가 인증하고 검증된 id_token을 `Authorization` 헤더로 넘겨주게** 할 수 있습니다. 앱마다 redirect URI를 등록하거나 client secret을 두지 않아도 됩니다 — IdP 앱은 프록시가 하나만 소유합니다.

```
DARAK_OIDC_ISSUER=https://login.microsoftonline.com/<tenant-id>/v2.0
DARAK_OIDC_CLIENT_ID=<프록시의 client id>   # 이 모드에선 토큰의 audience 로 쓰입니다
DARAK_OIDC_TENANT=<tenant-id>
DARAK_OIDC_EMAIL_DOMAINS=example.com
DARAK_SSO_FORWARD_AUTH=1                    # 코드 플로우 대신 프록시를 신뢰
```

`DARAK_SSO_FORWARD_AUTH=1`이면 `DARAK_OIDC_REDIRECT_URL`도 `DARAK_OIDC_CLIENT_SECRET*`도 필요 없습니다. 로그인 버튼(UI는 `/api/sso/login` 하나만 압니다)은 프록시가 지키는 `/api/sso/forward`로 넘기고, 거기서 프록시가 넘긴 Bearer id_token을 darak가 **테넌트 JWKS로 검증**한 뒤 나머지(신원 매핑·계정 게이트·자동 가입·세션)는 코드 플로우와 완전히 같은 길을 탑니다.

**신뢰 경계는 서명 검증입니다, 네트워크가 아니라.** darak를 프록시를 거치지 않고 직접 때려 헤더를 위조해도, 테넌트가 이 audience로 실제 발급한 토큰이 아니면 세션이 생기지 않습니다. 그래서 이 모드는 darak가 프록시 뒤에 있다고 **가정만** 하지 않고 매번 토큰을 검증합니다.

프록시가 `/api/sso/forward`에 `Authorization: Bearer <id_token>`을 실어 보내도록 미들웨어의 `authResponseHeaders`에 `Authorization`을 포함시키세요(oauth2-proxy는 `set_authorization_header = true`). 그리고 그 경로만 인증 미들웨어 뒤에 두어야 비밀번호 폴백(그 외 경로)이 살아 있습니다. **SMB(445)는 이 모드에서도 비밀번호뿐**입니다 — HTTP forward-auth가 낄 자리가 없습니다.

### 자동 프로비저닝 (선택)

기본은 꺼져 있고, 켜지 않으면 새 신원은 전부 관리자 승인을 기다립니다.

```
DARAK_PROVISION_CONFIG=/etc/darak/provision.yaml
```

**이것이 켜는 것은 이 시스템에서 유일하게 사람 없이 계정이 생기는 경로입니다.** darak이 계정을 만드는 것은 아닙니다 — 파일에 적힌 엔드포인트에 "만들어 달라"고 요청하고, 계정이 실제로 나타나는지(NSS + tdbsam) 기다렸다가 로그인시킵니다. 계정을 만드는 주체는 여전히 roster → usersync입니다.

흐름:

1. 매핑 없는 신원이 SSO로 들어옴 → 규칙이 일치하면 엔드포인트에 POST
2. **201 + `{"account":"..."}"`** → 그 계정이 나타날 때까지 대기(기본 30초) → 승인 없이 로그인 완료
3. 그 외(202/403/5xx/규칙 불일치) → 지금까지처럼 승인 대기 큐로

**계약과 각 응답 코드의 의미는 [`config/provision.yaml.example`](config/provision.yaml.example)에 전부 적혀 있습니다.**

지켜지는 것 두 가지:

- **이미 있는 계정에는 절대 자동 결합하지 않습니다.** 엔드포인트가 응답한 순간 이미 존재하던 이름이면 — 뭐라고 답했든 — 그것은 방금 만들어진 것이 아니므로 승인 큐로 갑니다. 망가진 엔드포인트가 쓰레기 계정을 만들 수는 있어도 **남의 홈 디렉터리를 넘겨줄 수는 없습니다.**
- **대기 시간을 넘겨도 실패가 아닙니다.** roster 반영이 늦으면 다음 로그인에서 완료됩니다.

설정 파일은 **재시작 없이 다시 읽힙니다.** 잘못 고치면 운영 페이지에 오류가 뜨고 **직전 규칙이 그대로 유지됩니다** — roster hot-reload와 같은 규칙입니다. 지금 어떤 버전이 적용 중인지(경로·반영 시각·내용 해시·규칙 목록)는 운영 페이지의 **회사 계정(SSO)** 패널에 표시됩니다.

**웹에서는 수정할 수 없고, 그럴 라우트도 없습니다.** 관리 페이지에서 엔드포인트 주소를 바꿀 수 있으면 그 페이지가 곧 자기 자신에게 계정을 발급하는 경로가 됩니다.

### 백업

`identities.json`(승인된 매핑)이 상태 볼륨에 있습니다. 잃으면 **모두 비밀번호 로그인으로 강등**될 뿐이고 파일 접근은 영향받지 않습니다 — 다시 승인하면 됩니다. `identity-journal.jsonl`은 누가 언제 어떤 매핑을 붙였는지의 기록이고, roster를 git에서 관리하는 대신 잃은 `git blame`을 대신합니다.

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
| 변경 | SMB 계정 잠금/해제, 비밀번호 재설정, 초기 비밀번호 조회 (전부 tdbsam만), **모든 팀의 구성원** |
| **불가** | 계정 생성·삭제, uid 변경, `status` 변경, 소유자 지정 — `roster.yaml`을 고쳐 커밋하고 재시작하세요 |

### 비밀번호

신규 사용자에게 넘길 **초기 비밀번호는 사용자 목록에서 바로 확인**할 수 있습니다(`초기 비밀번호` 버튼). 노드에 들어가 `usersync passwd`를 칠 필요가 없습니다.

그 값은 저장된 것을 읽는 것이 아니라 **시드 + 계정명으로 다시 계산**한 것입니다. 그래서:

- **본인이 바꾼 뒤에는 알 수 없습니다.** 버튼은 "변경됨"만 표시합니다.
- 화면에 값이 떴다면 **실제로 통하는 값입니다** — 내보내기 전에 `ntlm_auth`로 검증합니다. `usersync passwd`를 직접 치면 이미 바뀐 계정에도 옛 값을 그대로 출력하므로, 이쪽이 노드에서 치는 것보다 정확합니다.
- 조회는 **호출할 때마다 로그에 남습니다**(누가, 누구의 것을).

> **지금 비밀번호를 보는 방법은 없습니다.** tdbsam에는 NT 해시만 있고 되돌릴 수 없습니다 — root라도, 어떤 라우트를 추가하더라도 마찬가지입니다. 잊어버린 사람에게는 재설정이 유일한 답입니다.

비밀번호 재설정은 **잊어버린 사람을 위한 것**입니다. 평소에는 각자 ☰ 메뉴 → **비밀번호 변경**으로 직접 바꿉니다(지금 비밀번호를 요구하고, 그 사람의 다른 웹 세션을 닫습니다). 관리자든 본인이든 바뀌는 곳은 tdbsam 하나이므로 SMB 비밀번호가 같이 바뀝니다.

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

## 리버스 프록시 뒤에 둘 때

darak은 **경로 하나를 통째로** 받아야 합니다. 세 종류가 한 포트에 있고, 셋 다 같은 곳으로 가야 합니다:

| 경로 | 무엇 |
| --- | --- |
| `/api/…` | API 전부 — 로그인, 파일, 공유, 운영, 팀 |
| `/s/…` | 공유 링크. **로그인하지 않은 사람이 여는 곳**이라 인증을 앞단에서 걸면 안 됩니다 |
| 그 외 | 브라우저 인터페이스. 알 수 없는 경로는 `index.html`을 돌려주므로 `/homes/alice` 같은 깊은 링크가 새로고침에도 살아 있습니다 |

```nginx
server {
    listen 443 ssl;
    server_name files.example.com;
    ssl_certificate     /etc/ssl/files.pem;
    ssl_certificate_key /etc/ssl/files.key;

    # 업로드는 통짜 파일입니다. 기본값은 1MB이고, 버퍼링을 켜두면 nginx가
    # 파일 전체를 디스크에 받아쓴 뒤에야 darak에 넘깁니다.
    client_max_body_size 0;
    proxy_request_buffering off;

    location / {
        proxy_pass http://127.0.0.1:8080;
        # $host가 아니라 $http_host입니다. $host는 포트를 버리고, 공유 링크는
        # 요청의 Host로 만들어지므로 표준 포트가 아닌 곳에 두면 열리지 않는
        # 주소가 발급됩니다.
        proxy_set_header Host              $http_host;
        # 이게 없으면 공유 링크가 http://로 발급됩니다.
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
        proxy_http_version 1.1;
    }
}
```

그리고 `DARAK_BEHIND_PROXY=1`을 주세요. 세션 쿠키는 프록시 뒤에서도 `Secure`로 남습니다 — 브라우저와 프록시 사이가 HTTPS이므로 그게 맞습니다.

**서브패스에는 마운트할 수 없습니다.** `https://example.com/darak/` 같은 배치는 지금 동작하지 않습니다: 인터페이스가 `/api/…`를 절대 경로로 부르고, 번들도 `/assets/…`를 절대 경로로 참조합니다. 하위 도메인이나 전용 호스트를 쓰세요. 서브패스가 필요해지면 Vite의 `base`와 `api.ts`의 URL 조립을 함께 고쳐야 하는 일이지, 프록시 설정으로 해결되지 않습니다.

> 위 설정은 실제로 nginx를 앞에 세워 확인했습니다: 로그인 쿠키 왕복, `/homes/alice`·`/admin` 깊은 링크,
> 3MB 업로드, 그리고 발급된 공유 링크를 **로그아웃 상태로** 열어 3,000,000바이트를 받는 것까지.
> `$http_host` 주의사항은 그 과정에서 실제로 밟은 것입니다 — `$host`로 두면 `https://localhost/s/…`가
> 발급되어 아무 데도 닿지 않습니다.

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
