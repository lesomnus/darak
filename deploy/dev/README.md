# 개발 스택

인터페이스를 고치면서 **바로** 보기 위한 스택입니다. 서버는 빌드해서 돌리고, UI 는 `web/` 를 그대로 마운트한 Vite 가 냅니다.

```sh
docker compose -f deploy/dev/docker-compose.yaml up --build
```

**<http://localhost:5173>** 을 여세요. `alice` 와, darak 로그에 찍히는 비밀번호로 로그인합니다.

`web/src/` 아무 파일이나 저장하면 새로고침 없이 바뀝니다 — 측정해보면 저장에서 화면까지 0.25 초, 그 사이 전체 리로드 0 회, 검색창에 쳐둔 글자와 스크롤 위치가 그대로 남습니다.

---

## 두 컨테이너

| | 무엇 | 바꾸면 |
|---|---|---|
| `darak` | **빌드된** 서버. `-no-ui` 로 API 만 냅니다 | `up -d --build darak` 로 다시 올려야 합니다 |
| `web` | 빌드 **안 합니다.** 순정 node 이미지에 `web/` 를 mount 한 Vite | 저장하면 끝입니다 |

Vite 가 `/api/` 와 `/s/` 를 `http://darak:8080` 으로 프록시합니다. 그래서 브라우저 입장에서는 전부 `localhost:5173` 한 오리진이고, 세션 쿠키가 그냥 동작합니다.

### :8080 에는 화면이 없습니다

`-no-ui` 로 띄우기 때문입니다. 바이너리에 박혀 있는 인터페이스는 **`internal/ui/dist` 에 마지막으로 커밋된 것**이고, 그걸 살아 있는 쪽 옆에 같이 내놓으면 어느 쪽을 보고 있는지 알 수 없게 됩니다 — 고친 게 반영이 안 된다며 한나절을 보내는 경로가 정확히 그겁니다.

:8080 은 API 를 직접 찔러보라고 열어뒀습니다.

```sh
curl -s localhost:8080/api/branding
```

---

## 로컬 스택과의 관계

| | [`deploy/local`](../local/) | 여기 |
|---|---|---|
| 인터페이스 | 바이너리에 박힌 커밋된 빌드 | `web/` 에서 실시간 |
| 쓰는 곳 | 배포된 것처럼 굴어야 할 때, SMB 확인 | 화면을 고치는 동안 |
| 이미지 | `deploy/local/Dockerfile` | **같은 것** |
| roster · 로고 | `deploy/local/config` | **같은 것** |

이미지와 roster 를 공유하는 건 의도입니다. 계정 세팅·Samba·부팅 순서는 여기서도 `deploy/local` 이 돌리는 그것이어야 하고, 아니면 이 스택은 아무도 배포하지 않는 무언가를 테스트하게 됩니다.

**볼륨은 다릅니다.** compose 프로젝트 이름이 `darak-dev` 라서 `darak-dev_data` 와 `local_data` 는 별개입니다 — 파일도, 파생된 초기 비밀번호도 서로 다릅니다. 포트(8080, 1445)는 겹치므로 **둘을 동시에 띄울 수는 없습니다.** 하나 내리고 다른 걸 올리세요.

```sh
docker compose -f deploy/local/docker-compose.yaml down
docker compose -f deploy/dev/docker-compose.yaml up -d --build
```

---

## Go 를 고쳤을 때

```sh
docker compose -f deploy/dev/docker-compose.yaml up -d --build darak
```

`web` 은 건드리지 않으므로 브라우저 탭도, `npm ci` 도 그대로입니다.

---

## 볼 것이 있어야 화면을 보죠

데이터 볼륨은 비어 있는 채로 시작합니다. 목록·긴 이름 줄임표·아이콘 색을 보려면 alice 홈에 몇 개 만들어 두세요.

```sh
docker compose -f deploy/dev/docker-compose.yaml exec -T darak sh -c '
cd /srv/data/homes/alice
runuser -u alice -- mkdir -p 기획
runuser -u alice -- sh -c "
  head -c 674240 /dev/urandom > notes.txt
  head -c 557600 /dev/urandom > 발표자료.pdf
  head -c 493040 /dev/urandom > 로고.png
  head -c 411800 /dev/urandom > \"데모 영상.mp4\"
  head -c 532600 /dev/urandom > 예산안.xlsx
  head -c 342640 /dev/urandom > \"회의록 2026-08-07.md\"
  head -c 362280 /dev/urandom > backup-2026-08.tar.gz
  head -c 300000 /dev/urandom > \"아주 긴 이름을 가진 파일이라 줄임표가 필요한 문서.docx\"
"'
```

목록이 많을 때(가상 스크롤)를 보려면:

```sh
docker compose -f deploy/dev/docker-compose.yaml exec -T darak \
  runuser -u alice -- sh -c 'mkdir -p /srv/data/homes/alice/big && cd $_ && for i in $(seq 1 10000); do : > "file-$i.txt"; done'
```

---

## node_modules

호스트의 `web/node_modules` 는 **쓰지 않습니다.** 컨테이너의 것을 named volume 으로 그 위에 덮습니다.

이건 취향이 아니라 필요입니다. esbuild 와 rollup 은 플랫폼별 네이티브 바이너리를 따로 배포하므로, macOS 나 musl 머신에서 만든 트리를 이 이미지 안에 넣으면 첫 import 에서 깨집니다 — 그것도 **설치되어 있는 패키지 이름을 대면서** 없다고 하는 에러로요. 두 트리가 만나지 않게 하는 편이 낫습니다.

볼륨이라 재시작에도 남습니다. `web.sh` 가 `package-lock.json` 을 마지막 설치 시점과 비교해서 **달라졌을 때만** `npm ci` 를 돌립니다. 강제로 다시 하려면:

```sh
docker compose -f deploy/dev/docker-compose.yaml down
docker volume rm darak-dev_node_modules
```

---

## 어디에 열리나

**`127.0.0.1` 에만 열립니다** — 5173, 8080, 1445 전부. compose 의 `- "5173:5173"` 짧은 문법은 `0.0.0.0` 에 붙어서 같은 네트워크의 아무나 닿을 수 있고, 이 스택은 평문 HTTP 에 `-secure-cookies=false` 이고 비밀번호가 로그에 찍힙니다. 개발용으로는 다 맞는 선택이지만, 옆자리에서 닿는 건 아닙니다.

폰에서 화면을 확인하는 것처럼 정말 필요할 때만 엽니다:

```sh
DARAK_BIND=0.0.0.0 docker compose -f deploy/dev/docker-compose.yaml up
```

컨테이너끼리는 compose 네트워크로 통신하므로 이 설정과 무관합니다.

> [`deploy/local`](../local/docker-compose.yaml) 은 아직 `0.0.0.0` 입니다. 호스트 밖에서 SMB 를 마운트해보는 게 그 스택의 용도 중 하나라서 그대로 뒀습니다.

---

## macOS · Windows

Docker Desktop 의 bind mount 는 inotify 를 전달하지 않습니다. 파일을 저장해도 Vite 가 모르고, 아무 일도 일어나지 않습니다. 폴링을 켜세요:

```sh
VITE_POLL=1 docker compose -f deploy/dev/docker-compose.yaml up
```

CPU 를 쓰기 때문에 기본값이 아닙니다. Linux 에서는 필요 없습니다.

---

## 컨테이너 없이

`npm run dev` 는 그대로 됩니다. `DARAK_ORIGIN` 이 없으면 `http://localhost:8080` 으로 프록시하므로, 호스트에서 darak 을 돌리든 위 스택의 8080 을 쓰든 상관없습니다.

```sh
cd web && npm run dev
```
