# xquare-server

Go HTTP API 서버. 부모 디렉토리 CLAUDE.md의 규칙을 모두 따른다.

## 이 디렉토리에서 작업 시 추가 규칙

### 새 핸들러/기능 → `/feature-dev` 먼저 실행
예: "app 생성 API 만들어줘" → `/feature-dev` 실행 후 진행

### 패키지별 책임
```
internal/api/handler/   HTTP 핸들러 (비즈니스 로직 없음, 얇게)
internal/api/middleware/ JWT 인증, CORS, 에러 핸들링
internal/gitops/        GitOps YAML CRUD (go-git, sync.Mutex 필수)
internal/vault/         Vault KV v1 CRUD (v2 아님!)
internal/k8s/           K8s 상태조회 + 로그스트림 (client-go)
internal/github/        GitHub App API
internal/domain/        순수 도메인 구조체 (외부 의존 없음)
internal/config/        환경변수 로드
```

### 필수 패턴
- 모든 핸들러: `ctx context.Context` 전달
- GitOps 조작: 반드시 `sync.Mutex` (동시 write 방지)
- Vault: KV **v1** API (`/v1/xquare-kv/{path}`, data/ prefix 없음)
- 에러: `fmt.Errorf("operation: %w", err)` wrap 방식
- WebSocket 로그: goroutine으로 스트리밍, context cancel 처리

### 금지사항
- handler에서 직접 git/vault/k8s 호출 (반드시 internal/ 패키지 통해서)
- Vault KV v2 API 사용
- 하드코딩된 네임스페이스 (domain.Namespace() 함수 사용)
