# quota

Claude Code와 Codex CLI의 사용량(quota)을 조회하는 Go 도구.

## 바이너리

| 이름 | 설명 |
|------|------|
| `quota-cli` | CLI. quota 출력, quota 기반 비대화형 Claude/Codex 실행, 세션 로그 검색 |
| `quota-bar` | macOS 메뉴바 앱. 주기적으로 quota 갱신 표시 |

## 설치

```bash
go install github.com/sky1core/quota/cmd/quota-cli@latest
go install github.com/sky1core/quota/cmd/quota-bar@latest
```

이후 업데이트는 각자 수동으로:

- `quota-cli update` — 최신 릴리스로 quota-cli 재설치
- quota-bar 메뉴의 **Check for Updates…** — 최신 릴리스 설치 후 자동 재시작

### 시스템 요구사항

- `quota-cli` 지원 OS — macOS 또는 Linux. Windows는 지원하지 않는다.
- `claude` CLI — Claude Code CLI (PATH 또는 `~/.local/bin/claude`).
  `claude -p "/usage"`로 사용량을 조회하므로 그 명령을 지원하는 버전이어야 한다 —
  **2.1.214~2.1.235에서 확인**했다. 더 낮은 버전에서 동작하는지는 확인하지 않았다.
  구버전이면 Claude quota 조회만 실패하고 Codex 쪽은 영향받지 않는다.
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
  Week       79%   (5d 12h, at Mar 6 11:06)
  Fable     100%

Codex
  5h         79%   (31m, at 23:38)
  7d         63%   (5d 11h, at Mar 6 10:06)
  Reset credits: 4   (expires Mar 2 10:42)

Generated: 2026-02-28T23:06:50+09:00
```

#### 여러 계정 조회 (선택) — Claude / Codex

두 번째 Claude 계정 로그인부터 quota-bar 표시까지 전체 순서는
**[docs/multi-account.md](docs/multi-account.md)** 참고.

Claude 계정은 `CLAUDE_CONFIG_DIR`로, Codex 계정은 `CODEX_HOME`으로 구분된다. 기본 계정 외에 추가
계정을 함께 보려면 `account` 서브커맨드로 등록한다 (파일을 직접 편집할 필요 없음). **key 접두사로
provider가 정해진다** — `claude-<N>`은 Claude, `codex-<N>`은 Codex:

```bash
quota-cli account add claude-2 ~/.claude-2    # Claude 계정 등록 (dir = CLAUDE_CONFIG_DIR)
quota-cli account add codex-2  ~/.codex-alt   # Codex 계정 등록 (dir = CODEX_HOME)
quota-cli account list                        # 등록된 계정 확인 (Claude/Codex)
quota-cli account rm codex-2                   # 계정 제거
```

- `key`는 `claude-<N>` 또는 `codex-<N>` 형식이어야 한다. 형식·중복은 `add`가 검증한다.
- `dir`은 해당 계정의 config 디렉터리(Claude=`CLAUDE_CONFIG_DIR`, Codex=`CODEX_HOME`, `~` 확장 지원).
- **Codex는 각 `CODEX_HOME`에 별도 로그인**해 두어야 한다(`CODEX_HOME=~/.codex-alt codex login`). 인증 파일 복사가 아니다. 같은 과금 계정을 여러 home에 로그인해도 되지만, 사용량 한도·초기화권은 서버측 계정 단위라 숫자는 동일하게 나온다.
- **기본 계정은 실행 환경의 `CLAUDE_CONFIG_DIR`/`CODEX_HOME`을 그대로 따른다.** 그 변수가 설정된 셸(예: 에이전트 CLI 안)에서 `quota-cli`를 돌리면 기본 계정 행이 그 계정을 조회하므로, 같은 dir을 추가 계정으로도 등록해 두었다면 두 행에 같은 값이 나온다. 기본 계정을 고정해서 보려면 변수를 지우고 실행한다(`env -u CLAUDE_CONFIG_DIR quota-cli`).

등록하면 `quota-cli`가 기본 계정과 추가 계정을 함께 조회해 각각 `claude`/`claude-2`, `codex`/`codex-2` … 로
출력한다. 설정은 `~/.config/quota/config.json`에 저장되며, 직접 편집해도 된다:

```json
{
  "claudeAccounts": [ { "key": "claude-2", "configDir": "~/.claude-2" } ],
  "codexAccounts":  [ { "key": "codex-2",  "home": "~/.codex-alt" } ],
  "execPrompt": {
    "accountSettings": {
      "claude":   { "minLeftPct": 40 },
      "claude-2": { "minLeftPct": 5 },
      "codex":    { "minLeftPct": 30 },
      "codex-2":  { "minLeftPct": 5 }
    }
  }
}
```

#### quota 기반 프롬프트 실행

```bash
# 선택된 Claude 계정으로 `claude -p` 실행
quota-cli exec-prompt --agent=claude --model fable "프롬프트"

# 선택된 Codex 계정으로 `codex exec` 실행
quota-cli exec-prompt --agent=codex --json "프롬프트"
```

등록된 같은 provider 계정들의 60초 공유 캐시를 우선 사용하고, 필요한 계정만 quota를 실측한다.
적용되는 quota 창이 계정별 `minLeftPct` 미만인 계정은 후보에서 제외한다. 기본값은 5%이며,
선택 점수는 남은 quota에서 `minLeftPct`를 뺀 여유분이다. 장기 quota를 짧은 quota보다 우선하며,
여유분이 리셋까지 남은 시간에 비해 많은 계정을 먼저 쓴다.
Claude에 `--model`을 지정해도 요청 모델값과 실제 추가 quota row label이 맞을 때만 그 row를 본다. 현재 Opus처럼 전용 row가
없는 모델은 별도 quota를 가정하지 않고 `weekly_all`/`session`으로 비교한다. Fable처럼 해당 모델
quota의 남은 비율을 읽을 수 있으면 그 계정에는 하한선을 적용한다. 살아남은 모든 후보가 남은 비율을
읽을 수 있는 해당 모델 quota를 갖고 있을 때만 그 quota를 우선 비교하고, 일부 후보에만 있으면
`weekly_all`/`session`으로 비교한다. 선택이 끝나면 나머지 인자와 stdin/stdout/stderr, 종료 상태는 각각 `claude -p`와 `codex exec`에 그대로 전달된다.
Claude 후보는 적어도 `weekly_all` 또는 `session` quota를 갖고 있어야 한다.
대화형 실행은 지원하지 않는다.

#### 세션 로그 검색

```bash
quota-cli session-log list --agent=all --limit 20
quota-cli session-log search "검색어" --account claude-2 --limit 10
quota-cli session-log show <session-ref> --tail 40
```

등록된 Claude/Codex 계정 전체의 로컬 세션 로그를 읽기 전용으로 찾는다. 기본 출력은 user/assistant
텍스트만 포함하며, tool call/result 원문은 `--include-tools`를 지정한 경우에만 포함한다.
`search`는 기본 20개 결과와 220자 snippet만 출력하고, `show`는 기본 최근 40개 메시지와 메시지당
880자까지만 출력한다. `--json`으로 구조화 출력도 가능하다.

### quota-bar

```bash
quota-bar
```

메뉴바에서 항목을 체크하면 상단 바에 남은 %를 표시.

`quota-cli account add`로 추가 계정을 등록해 두면, quota-bar도 계정별 그룹(`Claude`, `Claude 2`, …,
`Codex`, `Codex 2`, …)으로 나눠 표시한다. quota-cli와 같은 `config.json`을 공유한다. 단, systray는
런타임에 메뉴 항목을 바꿀 수 없으므로 **계정 목록 변경은 quota-bar 재시작 후 반영**된다.

사용자 활동에 따라 갱신 주기가 자동 조절된다:
- 활성 사용 중: 3분 (`refreshActiveMinutes`로 변경 가능)
- 10분 이상 idle: 30분 (`refreshIdleMinutes`로 변경 가능)
- 1시간 이상 idle: 정지 (복귀 시 자동 재개)

두 주기는 `quota-bar.json`에 값을 쓰면 그 값을, 없으면 위 기본값을 쓴다. 변경은 재시작 후 반영된다.

설정 파일: `~/.config/quota/quota-bar.json`

메뉴의 "Start at Login" 체크로 로그인 시 자동 시작 + 비정상 종료 시 재시작을 설정할 수 있다.

## 빌드

```bash
go build -o quota-cli ./cmd/quota-cli
go build -o quota-bar ./cmd/quota-bar

# (선택) 재서명하면 System Settings에서 앱 이름이 정상 표시됨
# codesign -s - --force quota-bar
```

**버전 표시**: quota-bar 메뉴의 버전은 별도 플래그 없이 빌드 정보에서 자동으로 읽는다.
- `go install .../cmd/quota-bar@v0.7.0` → 태그 버전(`v0.7.0`) 표시
- 로컬 `go build`/`go install ./cmd/quota-bar` → 커밋 해시(예: `438784f`) 표시
- 명시 지정이 필요하면 여전히 `-ldflags "-X main.version=vX.Y.Z"`로 덮어쓸 수 있다.

## 테스트

```bash
go test ./...
```

## 라이선스

MIT
