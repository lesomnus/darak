# Kubernetes에 올릴 때

**먼저: darak은 클러스터 친화적인 앱이 아닙니다.** root로 돌고, 요청자 uid로 프로세스를 띄우고, 노드의 445를 잡고, 시작할 때마다 컨테이너의 `/etc/passwd`를 다시 만듭니다. 이 문서는 "그래도 올려야 한다면 무엇이 깨지는가"입니다.

돌아가게 만들 수는 있습니다. 다만 아래 항목들은 **선택이 아니라 조건**입니다.

---

## 계정은 어디에 있나 — 호스트에 영향이 없는 이유

가장 자주 오해받는 부분이라 먼저 적습니다.

**계정은 컨테이너 안에만 있고, 볼륨에 보관되지 않습니다. 매 기동마다 `roster.yaml`로부터 다시 만들어집니다.**

```
/etc/passwd, /etc/group, /etc/shadow   컨테이너 레이어. 볼륨 아님. 재시작하면 사라졌다 다시 생김
/var/lib/samba/                        볼륨 — private/passdb.tdb (비밀번호), secrets.tdb (AD 조인 상태)
/var/lib/darak/                        볼륨 — 공유 링크 토큰, 활동 기록, SSO 매핑·요청 큐
/srv/data/                             볼륨 — 파일
/etc/darak/                            roster.yaml, usersync.yaml  ※ 쓰기 가능해야 함 (아래)
```

호스트에는 아무것도 만들지 않습니다. 확인해보면 그렇습니다:

```console
$ id alice
id: 'alice': no such user          # 호스트
$ kubectl exec deploy/darak -- id alice
uid=3001(alice) gid=3001(alice)    # 컨테이너 안
```

### 왜 계정을 볼륨에 두지 않나

두 가지입니다.

**되지 않습니다.** `useradd`는 `/etc/passwd`를 임시 파일에 쓰고 `rename`으로 교체합니다. 파일 하나를 마운트하면 그 마운트는 inode에 묶여 있으므로, rename 하는 순간 마운트가 가리키는 것과 실제 파일이 갈라집니다.

**필요하지 않습니다.** **볼륨의 파일은 소유자를 이름이 아니라 번호로 압니다.** `/etc/passwd`는 그 번호에 이름을 붙이는 파생 상태일 뿐입니다. `roster.yaml`이 uid를 못 박고 있으므로, 매번 그 번호로 계정을 다시 만들면 파일은 계속 같은 사람의 것입니다.

> 그래서 **uid는 로스터에서 한 번 정하면 절대 바꾸면 안 됩니다.** 라이브 파일은 `chown -R`로 고쳐도 스냅샷은 불변이라 못 고칩니다.

### 그래서 백업은 두 개입니다

**비밀번호는 로스터에서 재생성할 수 없습니다.** 계정은 선언에서 나오지만 비밀번호는 아닙니다.

| 반드시 백업 | 왜 |
|---|---|
| `/srv/data` | 파일 |
| `/var/lib/samba` | **비밀번호.** 이것만 빠뜨리면 복구 후 아무도 로그인 못 합니다 |
| `roster.yaml` (git) | 계정 선언. uid 장부 |

`/var/lib/darak`은 잃어도 됩니다 — 공유 링크가 전부 죽고, SSO를 쓴다면 모두가 비밀번호 로그인으로 강등되는 정도입니다. 파일 접근은 영향받지 않습니다.

---

## 권장 형태 — 단일 노드 + hostPath

클러스터에서 원하는 것이 **선언적 관리**(매니페스트 한곳, GitOps, 롤아웃 이력)이고 고가용성이 아니라면, 이 배치가 맞습니다. 아래 10개 조건 중 **네 개가 그냥 사라집니다.**

```
스토리지 노드에 파드를 고정 + 데이터 디렉터리를 hostPath 로 마운트 + replicas 1
```

| 조건 | 이 배치에서 |
|---|---|
| 1. NFS 금지 | ✅ hostPath 는 로컬 파일시스템. **단 아래 함정** |
| 3. 파드 하나 | ✅ 의도한 대로 |
| 7. 노드 고정 | ✅ hostPath 가 강제 |
| 8. 로스터 쓰기 가능 | ✅ 설정 디렉터리도 hostPath 로 마운트하면 끝 |

남는 것은 **445 노출**(6번)과 아래 함정 하나입니다.

### ⚠️ hostPath 로 가리키는 데이터셋이 NFS 로도 export 되어 있으면 안 됩니다

hostPath 는 **PVC 계층의** NFS 문제를 없앨 뿐입니다. 그 디렉터리 자체가 NFS 로도 공유되고 있으면 우회는 그대로 살아 있습니다 — 마운트할 수 있는 누구든 uid 를 자칭합니다.

```sh
zfs get sharenfs <데이터셋>     # off 여야 함
exportfs -v | grep <경로>       # 아무것도 안 나와야 함
```

### 445

hostPath 로도 이건 안 풀립니다. 둘 중 하나:

```yaml
spec:
  hostNetwork: true          # 노드의 445·8080 을 그대로 씀. 가장 단순
```

또는 LoadBalancer(MetalLB·Cilium LB)로 전용 IP 를 주고 445 를 실어 보냅니다. `NodePort` 는 기본 범위가 30000–32767 이라 안 됩니다.

`hostNetwork` 를 쓰면 파드가 그 노드에 묶이는데, **이 배치에서는 어차피 묶여 있으므로 잃는 것이 없습니다.**

### 확인해 둘 것

| | |
|---|---|
| `cat /proc/self/uid_map` | `0 0 4294967295` 여야 합니다(4번). 다르면 darak 이 기동을 거부합니다 |
| PodSecurity | k3s 는 기본적으로 강제하지 않습니다. 켰다면 `privileged` 라벨이 필요합니다(5번) |
| 이미지 접근 | **GHCR 패키지는 레포가 공개여도 기본이 비공개입니다.** 패키지를 공개로 바꾸거나 `imagePullSecret` 을 주세요 |
| 445 를 쥔 프로세스 | 호스트 Samba 가 돌고 있으면 먼저 내려야 합니다 |

> 이 배치는 사실상 **compose 를 매니페스트로 옮긴 것**입니다. 그게 목적이라면 정직한 선택입니다 — 스케줄링·확장·무중단을 얻지는 못하지만, 설정이 선언으로 남고 롤아웃에 이력이 생깁니다.

---

## 반드시 맞춰야 하는 것

### 1. 볼륨은 블록이나 로컬. **NFS는 안 됩니다**

가장 위험한 항목입니다.

darak이 지키는 것은 "이 파일은 uid 3001의 것이고, 커널이 그 사람으로만 열어준다"입니다. **NFS는 클라이언트가 주장하는 uid를 그대로 믿습니다.** 데이터 볼륨이 NFS로도 노출되어 있으면, 그 마운트를 할 수 있는 누구든 `uid=3001`을 자칭해서 남의 홈을 엽니다. **darak이 하는 일 전체가 무의미해집니다.**

| StorageClass | |
|---|---|
| iSCSI / 블록 → ext4·xfs (RWO) | ✅ |
| `local` PV / hostPath (RWO) | ✅ |
| **NFS 기반 RWX** | ❌ **쓰지 마세요** |
| CephFS | ⚠️ 아래 `RENAME_NOREPLACE` 확인 필요 |

RWX가 필요하지도 않습니다 — 아래 이유로 **파드는 어차피 하나**입니다.

### 2. 파일시스템이 `renameat2(RENAME_NOREPLACE)`를 지원해야 합니다

삭제 경로가 여기 의존합니다. 휴지통 이름이 1초 해상도라 이름이 겹칠 수 있고, 겹쳤는지 알 방법이 이것뿐입니다(먼저 `stat`으로 보는 것은 경합입니다). **지원하지 않으면 darak이 삭제를 거부합니다** — 조용히 덮어쓰는 것보다 낫기 때문입니다.

ext4·ZFS는 확인했습니다. **실제로 쓸 PVC에서** 확인하세요 — 이미지에 있는 perl로 됩니다:

```sh
kubectl exec deploy/darak -- perl -e '
  my $d = "/srv/data";
  open my $f, ">", "$d/.rn-a"; close $f;
  open $f, ">", "$d/.rn-b"; close $f;
  # x86_64: renameat2=316, AT_FDCWD=-100, RENAME_NOREPLACE=1
  my $r = syscall(316, -100, "$d/.rn-a", -100, "$d/.rn-b", 1); my $e = $!+0;
  print $r==-1 && $e==17 ? "지원\n" : $r==-1 && $e==22 ? "미지원 — 삭제 불가\n" : "덮어씀 — 위험\n";
  unlink "$d/.rn-a", "$d/.rn-b";'
```

> 시스콜 번호가 아키텍처마다 다릅니다(위는 x86_64). arm64는 276입니다.
>
> 번거로우면 **darak으로 직접 확인하는 편이 확실합니다**: 아무 파일이나 올렸다 지워 보세요. 휴지통에 들어가면 되는 것이고, 지우기가 실패하면 이 플래그가 없는 것입니다. 실제로 도는 코드 경로를 그대로 지나가므로 위 스니펫보다 오해의 여지가 적습니다.

### 3. 파드는 하나. `Recreate`

```yaml
spec:
  replicas: 1
  strategy: { type: Recreate }
```

- 세션이 **프로세스 메모리**에 있습니다. 두 파드면 로그인이 절반씩 실패합니다.
- SMB 포트는 하나입니다.
- tdbsam도 하나여야 합니다. 둘이 같은 볼륨을 쓰면 서로 덮어씁니다.
- `RollingUpdate`는 잠깐 두 파드를 띄웁니다 — 그 사이가 위 전부입니다.

### 4. 사용자 네임스페이스 금지

```yaml
spec:
  hostUsers: true        # 기본값이지만 명시하세요
```

uid가 시프트되면 컨테이너의 3001이 디스크에서는 다른 번호가 되고, **로스터가 이름 붙이지 않은 번호의 소유로 파일이 쌓입니다. 그 시점에는 아무것도 실패하지 않습니다.** darak이 기동 시 `/proc/self/uid_map`을 읽어 **거부**하므로 사고로 이 상태가 되지는 않지만, 그러면 파드가 CrashLoop로 도는 것으로 나타납니다.

### 5. 권한 — restricted/baseline 네임스페이스에는 못 올립니다

darak은 요청자 uid로 헬퍼를 띄웁니다(`SysProcAttr.Credential`). root로 돌아야 하고, Samba도 그렇습니다.

```yaml
      securityContext:
        runAsUser: 0
        # 최소한 SETUID/SETGID가 필요합니다. Samba가 더 요구할 수 있으니
        # 먼저 privileged 로 띄워 확인하고 좁혀 가세요.
```

네임스페이스 라벨이 `pod-security.kubernetes.io/enforce: restricted` 또는 `baseline`이면 **파드가 아예 생성되지 않습니다.** `privileged` 네임스페이스에 격리해서 두세요.

### 6. 445를 어떻게 내보낼 것인가

`NodePort`는 안 됩니다 — 기본 범위가 30000–32767이라 445를 못 씁니다.

| | |
|---|---|
| **LoadBalancer** (MetalLB, Cilium LB 등) | 권장. 445와 8080을 전용 IP로 |
| **hostNetwork: true** | 됩니다. 대신 노드의 445·8080을 통째로 잡고 파드가 그 노드에 묶입니다 |

Windows·macOS 클라이언트는 **445 자체**를 원합니다. 다른 포트로는 붙지 않습니다.

### 7. 데이터가 있는 노드에 고정

로컬 PV나 hostPath를 쓰면 자동으로 묶이지만, 명시하는 편이 낫습니다:

```yaml
      nodeSelector:
        kubernetes.io/hostname: <데이터가 있는 노드>
```

### 8. `roster.yaml`은 ConfigMap으로 두면 안 됩니다

**ConfigMap 마운트는 읽기 전용입니다.** darak은 팀 소유자가 웹에서 구성원을 바꿀 때 `usersync member`로 **로스터의 그 한 줄을 실제로 편집**합니다. ConfigMap이면 그 기능이 실패합니다.

두 갈래입니다:

- **작은 PVC에 두고 git에서 동기화** — 팀 소유자 기능이 살아 있습니다. 대신 파드가 쓴 내용을 git으로 되돌리는 절차가 필요합니다
- **ConfigMap + 기능 포기** — 구성원 변경은 전부 로스터를 고쳐 재배포. 단순하지만, 그 변경이 잦으면 아무도 안 하게 됩니다

`usersync.yaml`은 읽기만 하므로 ConfigMap으로 충분합니다. **`seed.secret`은 Secret으로**, 읽기 전용이 맞습니다.

### 9. 프로브

`/`는 SPA를 돌려주므로 서버가 살아 있는지는 알려주지만 API가 도는지는 아닙니다. **`/api/branding`이 낫습니다** — 인증이 없고 200을 주며, 핸들러를 실제로 통과합니다.

```yaml
        readinessProbe:
          httpGet: { path: /api/branding, port: 8080 }
        livenessProbe:
          httpGet: { path: /api/branding, port: 8080 }
          initialDelaySeconds: 60      # 기동 시 usersync가 계정을 만들고 Samba가 뜹니다
```

> 인증이 필요한 라우트를 프로브로 쓰면 401이 오고, 그걸 실패로 읽습니다.

### 10. 종료를 기다려 주세요

업로드는 오래 걸립니다.

```yaml
      terminationGracePeriodSeconds: 60
```

---

## 최소 골격

빠지면 위 항목 중 하나가 깨지는 필드만 모은 것입니다. 완성된 매니페스트가 아닙니다.

```yaml
apiVersion: apps/v1
kind: Deployment
spec:
  replicas: 1
  strategy: { type: Recreate }
  template:
    spec:
      hostUsers: true                       # 4
      nodeSelector: { kubernetes.io/hostname: <노드> }   # 7
      terminationGracePeriodSeconds: 60     # 10
      containers:
        - name: darak
          image: ghcr.io/lesomnus/darak:edge
          securityContext: { runAsUser: 0 } # 5
          env:
            - { name: DARAK_BEHIND_PROXY, value: "1" }
            - { name: DARAK_ADMIN_MEMBERS, value: "<운영자>" }
          readinessProbe:                   # 9
            httpGet: { path: /api/branding, port: 8080 }
          volumeMounts:
            - { name: data,   mountPath: /srv/data }        # 1 — 블록/로컬
            - { name: samba,  mountPath: /var/lib/samba }   # 비밀번호
            - { name: state,  mountPath: /var/lib/darak }
            - { name: config, mountPath: /etc/darak }       # 8 — 쓰기 가능
            - { name: seed,   mountPath: /etc/darak/seed.secret, subPath: seed.secret, readOnly: true }
```

---

## 안 되는 것

| | |
|---|---|
| 수평 확장 | 세션이 메모리에 있고 SMB 포트도 tdbsam도 하나입니다 |
| 무중단 배포 | `Recreate`뿐입니다. 재시작마다 **전원 로그아웃**됩니다(세션이 메모리) |
| 노드 이동 | 데이터를 따라가야 합니다 |
| 읽기 전용 루트 파일시스템 | 매 기동마다 계정과 `smb.conf`를 씁니다 |

**재시작하면 전원 로그아웃**은 배포할 때마다 일어납니다. 파일은 멀쩡하고 다시 로그인하면 그만이지만, 알고 계셔야 합니다.

---

## 그냥 docker compose로 두는 편이 나은 경우

darak이 클러스터에서 얻는 것이 거의 없습니다 — 확장도 무중단 배포도 못 하고, 노드에 묶여 있고, 특권이 필요해 네임스페이스를 따로 격리해야 합니다. **스케줄러가 옮겨줄 수 없는 워크로드**입니다.

클러스터에 두는 이유가 "매니페스트를 한곳에서 관리하고 싶다"라면 그건 정당합니다. **"고가용성"이나 "확장"이라면 그건 얻을 수 없습니다.** [`deploy/prod`](../deploy/prod/README.md)의 compose 구성이 같은 일을 더 적은 부품으로 합니다.
