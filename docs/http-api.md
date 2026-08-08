# HTTP API

> 라우트 목록은 [README](../README.md) 에도 있다. 여기는 스펙 — 요청/응답 형태, 상태 코드, 그리고 **코드가 실제로 하는 일**이다.
>
> 이 문서의 모든 형태는 핸들러의 `json:"..."` 태그와 테스트에서 확인했고, 애매한 것은 실행 중인 스택에 직접 요청해서 확인했다. 확인하지 못한 것은 그렇게 적었다.

---

## 1. 형태

- **JSON over HTTP.** WebDAV 가 아니다. `PROPFIND`·`MKCOL`·`COPY`·`MOVE`·`LOCK` 은 없고, 디렉터리 목록은 그냥 그 경로에 `GET` 하면 JSON 으로 온다.
- **인증은 세션 쿠키 하나.** `darak_session`, `HttpOnly`, `Path=/`, `SameSite=Lax`, 그리고 배포가 그렇게 설정했으면 `Secure`. 헤더 토큰도, Basic 도, API 키도 없다.
- **CSRF 토큰은 없다.** 쿠키의 `SameSite=Lax` 가 유일한 방어다. 이 API 를 브라우저 밖에서 부를 계획이라면 알고 있어야 한다.
- **경로는 절대 경로로 고정.** `/api/…` 를 하드코딩하므로 서브패스 마운트가 안 된다([배포 문서](../deploy/prod/README.md) 참고).

세 가지 접두어가 한 포트에 있다.

| 접두어 | 인증 | 무엇 |
|---|---|---|
| `/api/…` | 세션 쿠키 | API 전부 |
| `/s/…` | **없음** — URL 이 곧 자격증명 | 공유 링크의 공개 측 |
| 그 외 | 없음 | 브라우저 인터페이스 (SPA) |

---

## 2. 모든 라우트에 걸리는 것

### 2.1 에러 봉투

```json
{ "error": "사람이 읽을 문장" }
```

`Content-Type: application/json; charset=utf-8`. **단, 예외가 둘 있다** — 아래 §2.3 과 `/s/` 계열은 `net/http` 의 평문 `404 page not found\n` 를 그대로 쓴다. 파서를 짤 때 JSON 을 가정하면 안 된다.

### 2.2 커널 errno → 상태 코드

파일 계열 전부가 이 표를 공유한다 (`server.go:380`).

| errno | 코드 | 본문 |
|---|---|---|
| `ENOENT`, `ENOTDIR` | 404 | `not found` |
| `EACCES`, `EPERM` | 403 | `not permitted` |
| `EEXIST` | 409 | `already exists` |
| `ENOTEMPTY` | 409 | `directory is not empty` |
| `EISDIR` | 400 | `is a directory` |
| `EXDEV` | 400 | `path is outside the served tree` |
| `ENOSPC`, `EDQUOT` | 507 | `out of space` |
| `ENAMETOOLONG` | 400 | `name too long` |
| 그 외 / `*vfs.Errno` 가 아님 | 500 | `internal error` |

**`EACCES` 는 403 이지 404 가 아니다.** 볼 수 없는 파일을 없는 척하는 것은 "누가 무엇을 알아도 되는가" 에 대한 **두 번째 규칙을 발명하는 것**이다. 커널의 판정을 숫자로 옮길 뿐 덮어쓰지 않는다. 존재를 숨기는 곳은 이 API 에 정확히 두 군데뿐이고(§5.1 의 revoke, §6 의 운영 표면), 둘 다 그렇게 하는 이유가 따로 있다.

### 2.3 등록되지 않은 메서드는 405 가 아니다 ⚠

SPA 가 `/` 에 catch-all 로 걸려 있어서, **매칭되는 라우트가 없는 요청은 인터페이스 핸들러로 떨어진다.**

```
POST    /api/files/homes/alice   ->  200  text/html   (index.html)
PATCH   /api/files/homes/alice   ->  200  text/html
OPTIONS /api/files/homes/alice   ->  200  text/html
GET     /api/dirs/homes/alice    ->  200  text/html
```

**로그인하지 않아도 그렇다.** 실행 중인 스택에서 확인했다.

클라이언트 입장에서 이게 왜 나쁜가: 메서드를 틀리면 **200 과 HTML 을 받는다.** 오타가 성공처럼 보인다. `405` 나 `Allow` 헤더에 기대면 안 되고, 응답이 JSON 인지 확인해야 한다.

> 이건 계약이라기보다 결함에 가깝다. `/api/` 에 catch-all 을 하나 등록해 404 로 끊는 것이 맞다 — [§9](#9-알려진-문제) 참고.

### 2.4 경로 피연산자

`/api/files/`·`/api/dirs/`·`/api/shares/` 뒤는 **`TrimPrefix` 한 원본 문자열**이다. 정규화하지 않는다.

- 리터럴 `..` 는 그 전에 mux 가 정리해 **307 리다이렉트**로 답한다.
- 퍼센트 인코딩된 `%2e%2e` 는 그대로 커널까지 가고 `RESOLVE_BENEATH` 가 거부한다 → 400 `path is outside the served tree`.
- 문자열 검사는 어디에도 없다. 경로의 의미는 커널이 정한다([ADR-4](../nas-design.md)).

### 2.5 세션

| | |
|---|---|
| 쿠키 이름 | `darak_session` |
| 수명 | `-session-ttl`, 기본 12시간 |
| 저장 | **프로세스 메모리** — 재시작하면 전부 무효 |
| 재로그인 | 기존 세션을 **무효화하지 않는다.** 새 토큰이 발급되고 옛 토큰도 만료까지 유효 |
| 레이트 리밋 | **없음.** 로그인 시도 제한·잠금·지연 전부 없다 |

`authed` 가 붙은 라우트의 401 은 두 가지다. 쿠키가 없으면 `not signed in`, 있는데 안 풀리면 `session expired` + 쿠키 삭제 `Set-Cookie`. **후자는 애초에 존재한 적 없는 토큰에도 나온다** — 메시지로 "유효했던 적이 있는지" 를 알 수 없다.

---

## 3. 인증

| | |
|---|---|
| `POST /api/login` | 인증 없음 |
| `POST /api/logout` | **인증 없음** |
| `GET /api/whoami` | 세션 |

### POST /api/login

요청 본문 `{"user": string, "password": string}`, 64 KiB 상한.

성공 **200** `{"user": "..."}` + `Set-Cookie`.

| 코드 | 언제 |
|---|---|
| 400 | JSON 파싱 실패, 빈 본문, 64 KiB 초과 → `malformed request`. **본문 초과는 413 이 아니다** |
| 401 | 자격증명 불일치 → `invalid username or password` |
| 503 | `ntlm_auth` 를 돌릴 수 없음 → `cannot verify credentials right now`. 401 과 구분하는 것이 요점이다 — "틀렸다" 와 "물어볼 수 없다" 는 다르다 |
| 500 | 세션 생성 실패(엔트로피) → `could not start a session` |

> 응답의 `user` 는 **요청 본문이 보낸 문자열을 그대로 되돌려준다.** 정규화하지 않는다. 실제 배포에서는 `auth.NTLM` 이 `^[a-z_][a-z0-9_-]{0,31}$` 로 거르므로 문제가 되지 않지만, 이 라우트 자체의 성질은 아니다.

### POST /api/logout

**`authed` 가 붙어 있지 않다.** 쿠키에 실려 온 토큰을 지우고 항상 **204** 를 준다 — 쿠키가 없어도, 모르는 토큰이어도, 이미 만료됐어도 204 다. 무엇이 일어났는지 호출자는 알 수 없다.

지우는 것은 **제시된 토큰 하나뿐**이다. 같은 사용자의 다른 세션은 살아 있다.

> 인증 게이트가 없으므로 `darak_session` 값을 실어 보내는 요청은 무엇이든 그 값을 무효화한다. 막아주는 것은 쿠키의 `SameSite=Lax` 뿐이다.

### GET /api/whoami

성공 **200** `{"user": "..."}`. 신원은 **오직 세션에서만** 온다 — `?user=`, `X-User`, `Authorization` 전부 무시된다(테스트가 고정하고 있다).

---

## 4. 파일과 디렉터리

| | |
|---|---|
| `GET /api/files/<path>` | 디렉터리면 JSON 목록, 파일이면 내용 |
| `PUT /api/files/<path>` | 업로드 |
| `DELETE /api/files/<path>` | **휴지통으로 이동** (§4.3) |
| `POST /api/dirs/<path>` | mkdir (재귀 아님) |

전부 세션만 요구한다. **권한 검사는 이 계층에 없다** — 요청은 그 사용자로 실행되고 커널이 판정한다.

### 4.1 GET — 디렉터리

```json
{
  "path": "homes/alice/",
  "entries": [
    { "name": "f.txt", "dir": false, "size": 10,
      "mod_time": "2026-08-08T18:22:57.274291428Z", "mode": "0600" }
  ]
}
```

- `entries` 는 **항상 배열**이다. 비어 있으면 `[]`, 절대 `null` 이 아니다.
- `mode` 는 `mode&0o7777` 의 네 자리 8진수 **문자열** — `"0600"`, `"2770"`.
- `path` 는 받은 피연산자를 **그대로** 되돌려준다(끝의 `/` 포함).
- `.upload-*` (업로드 중인 임시 파일)는 **무조건 숨긴다.** `.trash` 와 그 내용은 **보인다** — 그게 되돌리는 방법이다.
- 페이지네이션·커서가 **없다.** 큰 디렉터리는 하나의 큰 JSON 이다.

> **`mode` 가 빈 문자열이면 "모름" 이지 0 이 아니다.** 헬퍼의 엔트리별 stat 이 실패하면 그 행은 조용히 열화된다 — `size` 0, `mode` `""`, `mod_time` `"0001-01-01T00:00:00Z"`.

### 4.2 GET — 파일

원본 바이트. `http.ServeContent` 에 위임하므로 **Range(206) / If-None-Match(304) / If-Modified-Since / 416 / 412** 가 전부 동작한다.

핸들러가 붙이는 헤더: `ETag: "<ino>-<size>-<mtime>"` (전부 16진수), `X-Content-Type-Options: nosniff`, `Content-Disposition: attachment; filename*=UTF-8''…`.

### 4.3 DELETE 는 삭제가 아니다 ⚠

부모 디렉터리 이름이 문자 그대로 `.trash` 인 경우를 빼면, 엔트리는 **이름이 바뀔 뿐이다**:

```
<domain>/.trash/<UTC yyyy-mm-ddTHH-MM-SS>_<basename>
```

여전히 공간을 차지하고, `GET` 으로 목록에 뜨고 내려받을 수 있으며, SMB 에도 보인다. **`.trash` 안의 것을 다시 `DELETE` 해야 진짜 지워진다.**

디렉터리도 204 로 통째로 휴지통에 들어간다 — 재귀 플래그도 확인도 없다. 그런데 **일단 휴지통에 들어간 디렉터리는 이 API 로 지울 수 없다.** unlink 경로가 `AT_REMOVEDIR` 를 넘기지 않아서 영원히 400 `is a directory` 다.

> **데이터 손실 경계:** 휴지통 이름은 **1초 해상도**다. 덮어쓰기 경로와 달리 `Remove` 는 이름 충돌 시 재시도하지 않는다. 같은 basename 을 1초 안에 두 번 지우면 두 번째가 첫 번째 휴지통 사본을 **조용히 덮어쓴다**(고정 시계로 확인: 두 번째 내용만 남았다). 단, 대상이 **비어 있지 않은 디렉터리**면 덮어쓰는 대신 409 `directory is not empty` 로 실패한다.

### 4.4 PUT 이 하는 일

메서드+경로만 봐서는 예상하기 어려운 것들:

1. **이전 판을 남긴다.** rename 전에 옛 inode 를 `<domain>/.trash/` 로 하드링크한다. 덮어쓰기는 **블록을 반환하지 않는다.**
2. `.trash` 가 없으면 만든다(홈은 0700, 팀은 2770 setgid).
3. 대상의 부모 디렉터리에 `.upload-<16자 base32>` 임시 파일을 만들고 rename 한다. 이 API 목록에는 안 보이지만 **SMB 에는 보인다.** 크래시하면 남는다.
4. **mode 는 호출자가 정할 수 없다** — 경로가 정한다. 홈 아래 0600, 팀 아래 0660.
5. 결과는 **새 inode** 이므로 이전 `ETag` 는 무효가 된다.

조건부 쓰기가 **없다.** `If-Match` 를 보지 않는다. 동시 PUT 은 last-write-wins 이고, 진 쪽의 판은 휴지통에 남는다([ADR-6](../nas-design.md)).

성공은 **204, 본문 없음.** 크기도 ETag 도 돌려주지 않는다.

> **본문이 `-max-upload`(기본 64 GiB)를 넘으면 500 `internal error`** 다. 413 이 아니다. `http.MaxBytesError` 가 `*vfs.Errno` 가 아니라 §2.2 표의 default 로 떨어지기 때문이다. 의도된 계약이 아니라 코드의 현재 동작이다.

### 4.5 POST /api/dirs — 재귀가 아니다

정확히 **한 개**를 만든다. 중간 부모가 없으면 404 다. `mkdir -p` 가 아니다.

mode 는 경로가 정한다 — 홈 0700, 팀 02770(setgid). 본문은 **읽지도 않는다**(JSON 을 보내도 무시하고 204).

### 4.6 이 그룹의 오류 특이점

| 상황 | 답 | 비고 |
|---|---|---|
| 피연산자가 빔 (`/api/files/`) | 400 `no path` | |
| 권한 도메인 밖 (PUT, POST dirs) | 400 | Go 에러 원문이 새어나온다: `vfs: "stray.txt" is not inside a permission domain` |
| 권한 도메인 밖 (**DELETE**) | **500** `internal error` | 같은 상황인데 답이 다르다. `handleDelete` 가 자체 도메인 검사를 안 한다 |
| 끝에 `/` (PUT/DELETE/POST dirs) | **500** `internal error` | 헬퍼의 `EINVAL` 이 표에 없어 default 로 떨어진다 |

마지막 두 줄은 **의도된 계약이 아니라 코드의 현재 동작**이다.

---

## 5. 공유 링크

### 5.1 내부 (세션 필요)

| | |
|---|---|
| `POST /api/shares` | 발급 |
| `GET /api/shares` | 내 링크 목록 |
| `DELETE /api/shares/<token>` | 폐기 |

**POST** 본문 `{"path": string, "password": string, "ttl_hours": int}`.
`password` 가 비면 암호 없는 링크. `ttl_hours` 는 `<=0` 이면 기본 7일, **30일을 넘으면 조용히 30일로 깎인다** — 범위 초과는 에러가 아니다.

성공 **200**(201 아님):

```json
{ "token": "...", "url": "https://host/s/...", "path": "...",
  "name": "basename", "created": "...", "expires": "...", "protected": false }
```

`url` 은 **요청의 `Host` 와 `X-Forwarded-Proto` 로 만든다** — 프록시 설정이 여기에 직결된다([배포 문서](../deploy/prod/README.md)).

| 코드 | 언제 |
|---|---|
| 501 | 공유 기능이 꺼짐 → `sharing is not enabled` |
| 400 | 대상이 디렉터리 → `a link can only point at a file` |
| 403/404 | 발급자가 그 파일을 읽을 수 없음. **여기서는 403 을 정직하게 준다** |

> 발급자가 읽을 수 없는 것은 링크로 만들 수 없다 — stat 후 실제로 열어보고 바로 닫는 것이 그 확인이다.

**GET** 은 `{"links": [...]}`. 만료된 것은 걸러지고, 최신순이며, 남의 것은 **조용히 없다**(에러가 아니라 누락). 공유 기능이 꺼져 있어도 **501 이 아니라 200 `{"links":[]}`** 를 준다 — 링크가 없는 사용자와 구분되지 않는다.

**DELETE** 는 성공 204. 실패는 **전부 404 `no such link`** — 없는 토큰, 만료, 이미 폐기, **남의 링크**, 빈 토큰이 전부 같은 답이다. 여기가 존재를 숨기는 두 곳 중 하나다. 토큰이 존재하는지, 누구 것인지 탐침할 수 없게 하는 것이 목적이다. **관리자 우회는 없다** — 남의 링크는 관리자도 못 지운다.

두 번째 DELETE 는 204 가 아니라 404 다(멱등하지 않다).

### 5.2 공개 측 (`/s/<token>`) — 세션 없음

**`GET /s/<token>`**

- 암호 없는 링크, 또는 유효한 해제 쿠키: **200 + 원본 바이트.** Range/조건부 요청 전부 동작.
- 암호 걸린 링크에 해제 쿠키가 없으면: **200**(401 이 아니다) + 해제 폼 HTML.
- 실패는 전부 **평문** `404 page not found` — 기능 꺼짐, 없는 토큰, 만료, 폐기, 파일 삭제됨, 소유자가 권한을 잃음이 **바이트 단위로 동일**하다. 이 라우트는 `writeFSError` 를 거치지 않으므로 **403 을 절대 주지 않는다** — `/api/files/` 의 규약과 정반대다.

**`POST /s/<token>`** 은 `application/x-www-form-urlencoded` 의 `password` 필드다(JSON 아님, 쿼리 파라미터 아님).

- 암호 맞음 → **303** + `Set-Cookie: darak_unlock_<token>`(`Path=/s/<token>`, 링크 만료와 함께 만료).
- 암호 틀림 → **401** + 폼 HTML.

암호 비교는 상수 시간이다. 하지만 **시도 횟수 제한이 없다** — 토큰에 대한 암호 추측은 무제한이다.

> **암호 없는 링크에 POST 하면 파일이 내려온다.** `Protected()` 가 false 면 리다이렉트 블록을 통째로 건너뛰고 본문 스트리밍으로 떨어진다. 링크를 탐침하려고 POST 하면 다운로드하게 된다.

> **만료된 토큰에 GET 하면 파괴적이다.** `Resolve` 가 만료 항목을 메모리 맵에서 지운 뒤 `ErrNotFound` 를 돌려준다. 이 삭제는 디스크에 저장되지 않으므로, **인증 없는 GET 이 메모리와 파일을 조용히 어긋나게 만든다.**

> 해제 쿠키의 HMAC 키는 **프로세스마다 새로 생성**된다. 재시작하면 모든 해제 쿠키가 무효가 되고 암호를 다시 묻는다.

---

## 6. 운영 표면 (`/api/admin/…`)

`GET /api/admin/whoami` 만 **모든 로그인 사용자**가 쓸 수 있다 — `{"admin": bool, "group": string}`. 나머지는 전부 `admin` POSIX 그룹이 필요하다.

| | |
|---|---|
| `GET /api/admin/users` | 계정 인벤토리 |
| `GET /api/admin/disk` | 용량 + uid별 사용량 |
| `GET /api/admin/audit` | roster 대조 |
| `GET /api/admin/activity` | 활동 기록 |
| `POST /api/admin/users/<user>/smb` | `{"enabled": bool}` → 204 |
| `POST /api/admin/users/<user>/password` | `{"password": string}` → 204 |

**자격 없는 호출자는 403 이 아니라 평문 404 를 받는다.** 운영 API 가 이 경로에 존재한다는 사실 자체를 확인해주지 않는다 — 그래서 아무도 자격이 없는 배포와 구분되지 않는다.

`activity` 의 쿼리 파라미터: `user`(정확히 일치), `path`(부분 문자열), `action`(`create|write|delete|rename|mkdir`), `days`, `limit`. **숫자가 아니거나 0 이하면 에러가 아니라 조용히 무시된다.**

`smb` 를 자기 계정에 대해 `false` 로 하면 **409** `refusing to disable your own account`.

`503` 은 두 종류가 섞여 있다. 관리자 그룹 확인 자체를 못 한 경우와, `smbpasswd`/`usersync` 가 실패한 경우다. 후자는 하위 명령의 에러 원문이 그대로 실려 나온다.

> **`smb` 키가 없다는 것은 두 가지 뜻이다.** 구조체 주석은 "Samba 에 물어볼 수 없었음" 만을 뜻한다고 적고 있지만, 코드는 **pdbedit 이 정상 응답했는데 그 이름의 레코드가 없는 경우에도** `nil` 로 둔다(`internal/admin/users.go:150`). 앞의 경우에만 `warnings` 가 붙으므로, 경고 유무로도 완전히 구분되지 않는다. [§9](#9-알려진-문제) 참고.

---

## 7. 팀

| | |
|---|---|
| `GET /api/teams` | 팀·소유자·구성원·선택 가능한 사용자 |
| `GET /api/teams/whoami` | `{"teams": [내가 소유한 팀]}` |
| `POST /api/teams/<team>/members` | `{"user": string, "member": bool}` → 204 |

앞의 둘은 **모든 로그인 사용자**가 쓸 수 있다. 대부분에게 `whoami` 의 답은 빈 배열이다.

`POST` 는 **그 팀의 소유자이거나 `admin` 그룹**이어야 한다. 아니면 **평문 404** — 운영 표면과 같은 이유로 존재를 확인해주지 않는다. 반면 대상 사용자가 roster 에 없으면 JSON `{"error":"no such managed account"}` 로 404 다. 같은 상태 코드에 본문 형태가 다르다.

`member` 는 `*bool` 이라 **반드시 있어야 한다.** 없거나 `null` 이면 400.

이 호출은 `usersync member` 를 거쳐 roster 를 **실제로 편집한다.** 주석과 서식은 보존된다.

---

## 8. 이 API 가 하지 않는 것

- **이름 변경/이동이 없다.** 와이어 프로토콜에는 `OpRename` 이 있지만 HTTP 로 노출되지 않는다. 옮기려면 받아서 다시 올리고 지워야 한다.
- **복사가 없다.**
- **조건부 쓰기가 없다.** `If-Match` 를 PUT 이 보지 않는다.
- **일괄 연산이 없다.** 파일 하나에 요청 하나다.
- **목록 페이지네이션이 없다.**
- **계정 생성·삭제·uid 변경이 없다.** roster.yaml 의 몫이다([ADR-8](../nas-design.md)).
- **레이트 리밋이 없다.** 로그인에도, 공유 링크 암호에도.
- **CSRF 토큰이 없다.**

---

## 9. 알려진 문제

문서화하면서 확인된 것들이고, 계약이 아니라 고쳐야 할 것으로 본다.

1. **등록되지 않은 메서드가 200 + HTML 을 준다**(§2.3). `/api/` 에 catch-all 을 등록해 404 로 끊어야 한다. 지금은 클라이언트의 메서드 오타가 성공처럼 보인다.
2. **`smb` 키의 부재가 두 가지를 뜻한다**(§6). `internal/admin/users.go:150` 이 "물어볼 수 없었음" 과 "계정이 없음" 을 같은 `nil` 로 만든다. 구조체 주석은 전자만 뜻한다고 적고 있어 코드와 어긋난다. 관리 페이지는 이 값을 "확인 불가" 로 렌더하므로, **Samba 계정이 없는 관리 대상 사용자가 "확인 불가" 로 보인다.**
3. **같은 상황에 다른 코드**(§4.6). 권한 도메인 밖은 PUT 에서 400, DELETE 에서 500 이다. 끝의 `/` 도 500 이다.
4. **업로드 초과가 500 이다**(§4.4). 413 이어야 한다.
5. **휴지통 이름의 1초 해상도**(§4.3)가 데이터 손실 경계를 만든다.
