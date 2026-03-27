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
  Sonnet    100%
  Extra      65%   (52m, at 23:59)   $17.91/$50.00

Codex
  5h         79%   (31m, at 23:38)
  Day        63%   (5d 11h, at Mar 6 10:06)

Generated: 2026-02-28T23:06:50+09:00
```

### quota-bar

```bash
quota-bar
```

메뉴바에서 항목을 체크하면 상단 바에 남은 %를 표시.

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

# 빌드 후 재서명 (macOS TCC가 Bundle ID를 인식하도록)
codesign -s - --force quota-bar
```

## 테스트

```bash
go test ./...
```

## 라이선스

MIT
