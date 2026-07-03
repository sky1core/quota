# quota

Claude Code와 Codex CLI의 사용량(quota)을 조회하는 Go 도구.

## 바이너리

| 이름 | 설명 |
|------|------|
| `quota-cli` | CLI. JSON 또는 텍스트로 quota 출력 |
| `quota-bar` | macOS 메뉴바 앱. 주기적으로 quota 갱신 표시 |

## 설치

```bash
go install github.com/sky1core/quota/cmd/quota-cli@latest
go install github.com/sky1core/quota/cmd/quota-bar@latest
```

### 시스템 요구사항

- `tmux` — Claude quota 조회에 필요 (`brew install tmux`)
- `claude` CLI — Claude Code CLI (`~/.local/bin/claude` 또는 PATH)
- `codex` CLI — Codex CLI (PATH)

## 사용법

### quota-cli

```bash
# 텍스트 출력
quota-cli

# JSON 출력
quota-cli --json

# 타임아웃 지정 (기본 40초)
quota-cli --timeout 60
```

출력 예시:
```
Claude
  Session    98%   (4h 52m, at 03:59)
  Weekly     79%   (5d 12h, at Mar 6 11:06)
  Fable     100%

Codex
  5h         79%   (31m, at 23:38)
  Day        63%   (5d 11h, at Mar 6 10:06)

Generated: 2026-02-28T23:06:50+09:00
```

#### 여러 Claude 계정 조회 (선택)

두 번째 Claude 계정 로그인부터 quota-bar 표시까지 전체 순서는
**[docs/multi-account.md](docs/multi-account.md)** 참고.

Claude 계정은 `CLAUDE_CONFIG_DIR`로 구분된다. 기본 계정 외에 추가 계정을 함께 보려면
`account` 서브커맨드로 등록한다 (파일을 직접 편집할 필요 없음):

```bash
quota-cli account add claude-2 ~/.claude-2   # 계정 등록
quota-cli account list                       # 등록된 계정 확인
quota-cli account rm claude-2                # 계정 제거
```

- `key`(`claude-2`)는 `claude-<N>` 형식이어야 한다(소비자가 추가 provider로 인식). 형식·중복은 `add`가 검증한다.
- `configDir`(`~/.claude-2`)는 해당 계정의 Claude config 디렉터리다(`~` 확장 지원).

등록하면 `quota-cli`가 기본 계정과 추가 계정을 함께 조회해 각각 `claude`, `claude-2` … 로 출력한다.
설정은 `~/.config/quota/config.json`에 저장되며, 직접 편집해도 된다:

```json
{ "claudeAccounts": [ { "key": "claude-2", "configDir": "~/.claude-2" } ] }
```

### quota-bar

```bash
quota-bar
```

메뉴바에서 항목을 체크하면 상단 바에 남은 %를 표시.

`quota-cli account add`로 추가 계정을 등록해 두면, quota-bar도 계정별 그룹(`Claude`, `Claude 2`, …)으로
나눠 표시한다. quota-cli와 같은 `config.json`을 공유한다. 단, systray는 런타임에 메뉴 항목을 바꿀 수
없으므로 **계정 목록 변경은 quota-bar 재시작 후 반영**된다.

사용자 활동에 따라 갱신 주기가 자동 조절된다:
- 활성 사용 중: 3분
- 10분 이상 idle: 30분
- 1시간 이상 idle: 정지 (복귀 시 자동 재개)

설정 파일: `~/.config/quota/quota-bar.json`

메뉴의 "Start at Login" 체크로 로그인 시 자동 시작 + 비정상 종료 시 재시작을 설정할 수 있다.

## 빌드

```bash
go build -o quota-cli ./cmd/quota-cli
go build -o quota-bar ./cmd/quota-bar

# 버전 정보를 포함하여 빌드
go build -ldflags "-X main.version=v0.4.0" -o quota-bar ./cmd/quota-bar

# (선택) 재서명하면 System Settings에서 앱 이름이 정상 표시됨
# codesign -s - --force quota-bar
```

## 테스트

```bash
go test ./...
```

## 라이선스

MIT
