# quota — Claude Code & Codex CLI Quota Monitor

## 개요

Go로 작성된 Claude Code와 Codex CLI의 사용량(quota) 조회 도구.
두 개의 독립적인 바이너리를 제공한다:

| 바이너리 | 설명 |
|----------|------|
| `quota-cli` | CLI 도구. JSON 또는 텍스트로 quota 출력 |
| `quota-bar` | macOS 메뉴바(systray) 앱. 주기적으로 quota 갱신하여 표시 |

- 둘은 **독립적인 프로그램**이다. 서로를 호출하지 않는다.
- 둘 다 **동일한 internal 패키지를 직접 호출**하여 데이터를 가져온다.

---

## 프로젝트 구조

```
github.com/sky1core/quota
├── cmd/
│   ├── quota-cli/main.go    # CLI 엔트리포인트
│   └── quota-bar/main.go    # macOS systray 엔트리포인트
├── internal/
│   ├── claude/claude.go     # Claude Code quota 조회
│   ├── codex/codex.go       # Codex CLI quota 조회
│   ├── render/render.go     # 텍스트 출력 포맷터
│   └── ui/icon.go           # systray 아이콘 (22x22 PNG)
├── go.mod
├── go.sum
└── SPEC.md
```

---

## 데이터 모델

### Claude quota

`internal/claude` 패키지가 반환하는 `map[string]any`:

```json
{
  "session":   { "used": 5, "left": 95, "resetsIn": "4h 30m" },
  "weeklyAll": { "used": 10, "left": 90, "resetsIn": "2d 5h" },
  "extras": [
    { "label": "Fable", "used": 20, "left": 80, "resetsIn": "2d 5h" }
  ]
}
```

각 항목:
- `used` (int): 사용 퍼센트 (0–100)
- `left` (int): 남은 퍼센트 (100 - used)
- `resetsIn` (string, optional): 리셋까지 남은 시간 (예: `4h 30m`, `2d 5h`)

`session`(Current session)과 `weeklyAll`(Current week (all models))은 구조적 고정 키다.
그 외의 `/usage` 행은 모델 세대에 따라 라벨이 바뀌므로(예: `Sonnet only` → `Fable`)
고정 키 대신 `extras` 배열(`[]map[string]any`)로 반환한다:
- `label` (string): 화면에 표시된 라벨. `Current week (X)` 형식이면 괄호 안 `X`, 아니면 라벨 줄 전체
- 화면 표시 순서를 유지한다
- 같은 라벨이 중복 등장하면 첫 항목만 채택

**계정 단위**: `internal/claude`가 반환하는 이 구조는 Claude 계정 **하나**의 quota다.
계정은 `CLAUDE_CONFIG_DIR`(Claude CLI config 디렉터리)로 구분된다 — config-dir이 다르면 다른 계정이다.
config-dir 미지정 시 claude CLI 기본 계정(`~/.claude` 또는 프로세스의 `CLAUDE_CONFIG_DIR`)을 조회한다.
여러 계정을 합쳐 출력하는 것은 `quota-cli`의 책임이며, `internal/claude`는 계정 분리 메커니즘을 알지 못한다.

### Codex quota

`internal/codex` 패키지가 반환하는 `map[string]any`:

```json
{
  "fiveHour": { "left": 85, "resetsIn": "2h 30m" },
  "day":      { "left": 70, "resetsIn": "18h 5m" },
  "credits":  { "balance": "100.00", "hasCredits": true, "unlimited": false },
  "planType": "pro"
}
```

각 항목:
- `fiveHour`: 5시간 윈도우 rate limit
- `day`: 일일 윈도우 rate limit
- `left` (int): 남은 퍼센트 (100 - usedPercent)
- `resetsIn` (string): 리셋까지 남은 시간 (예: `2h 30m`, `45m`, `1d 3h`)

**공통**: Claude와 Codex 모두 리셋 시간은 `resetsIn` 키로 **남은 시간**(상대시간)을 반환한다. 절대시간 금지.

---

## 바이너리 사양

### quota-cli

**용도**: 터미널에서 quota를 한번 조회하고 결과 출력

**플래그** (조회 모드):
| 플래그 | 기본값 | 설명 |
|--------|--------|------|
| `--json` | false | JSON 포맷으로 출력 |
| `--timeout` | 40 | 타임아웃 (초) |

**서브커맨드** (`account`): 추가 Claude 계정을 `config.json`에 관리한다. 손으로 파일을 편집하지 않아도 된다.
| 명령 | 설명 |
|------|------|
| `quota-cli account list` | 등록된 계정 목록과 config 경로 출력 (기본 `claude` 포함) |
| `quota-cli account add <key> <configDir>` | 계정 추가. `key`는 `claude-<N>` 형식, `configDir`는 `~` 확장 지원. 검증(형식·중복 key·중복 configDir)을 통과해야 저장하며, `configDir`가 없으면 경고만 하고 진행한다. configDir는 유저가 쓴 그대로 저장한다. |
| `quota-cli account rm <key>` | 계정 제거 |

- `account` 첫 인자가 아니면 조회 모드로 동작한다(기존 동작).
- 검증 규칙은 조회 시 `config.json`을 읽는 규칙과 동일하다(같은 형식/중복 규칙).

**동작**:
1. `~/.config/quota/config.json`에서 추가 Claude 계정 목록을 읽는다 (파일 없거나 목록 비면 기본 계정만).
2. 기본 Claude 계정 + 추가 계정을 각각 `claude.GetQuotaForConfigDir`로 **병렬** 조회
3. `codex.GetQuota(timeout)` 호출
4. 모두 완료되면 결과를 JSON 또는 텍스트로 출력
5. 개별 provider/계정 에러는 errors 배열에 포함, 프로세스 자체는 종료하지 않음

**JSON 출력 형식**:
```json
{
  "claude":   { ... },
  "claude-2": { ... },
  "codex":    { ... },
  "errors": []
}
```
- 기본 Claude 계정은 항상 `claude` 키.
- 추가 계정은 `config.json`에 명시한 `key`(예: `claude-2`)로 top-level에 배치한다. 각 계정 값은 Claude quota 구조(`session`/`weeklyAll`/`extras`)와 동일하다.
- 계정 조회 실패는 해당 top-level 키를 생략하고 `errors`에 계정 key와 함께 기록한다 (부분 결과 허용).

**설정 파일**: `~/.config/quota/config.json`
```json
{
  "claudeAccounts": [
    { "key": "claude-2", "configDir": "~/.claude-2" }
  ]
}
```
- `claudeAccounts` (optional): 기본 계정 외에 추가로 조회할 Claude 계정 목록.
  - `key` (string, 필수): 출력 top-level 키. **`claude-<정수>` 형식이어야 한다**(정규식 `^claude-\d+$`, 예: `claude-2`, `claude-3`). 기본 계정 `claude` 및 다른 항목과 중복 불가. (sky-ai 등 소비자는 `^claude-?\d+$`로 추가 provider를 인식한다.)
  - `configDir` (string, 필수): 해당 계정의 Claude config 디렉터리. `~`는 홈으로 확장된다. 유저가 직접 지정하며, 외부 도구의 설정을 참조하지 않는다. 서로 다른 계정은 서로 다른 `configDir`를 가리켜야 한다.
- 파일이 없거나 `claudeAccounts`가 비면 기본 계정만 조회한다(기존 동작).
- 다음 항목은 건너뛰고 `errors`에 기록한다: 빈 `key`/`configDir`, `claude-<정수>` 형식 위반, 중복 `key`, 중복 `configDir`. (중복 `configDir`는 계정별 tmux 세션명이 충돌하므로 금지.)

### quota-bar

**용도**: macOS 메뉴바에 상주하며 quota를 주기적으로 갱신

실행 시 자동으로 백그라운드 프로세스로 전환된다 (`&` 불필요).
중복 실행 방지: `~/.config/quota/quota-bar.pid` 파일에 flock을 획득하여 단일 인스턴스만 실행된다. 이미 실행 중이면 즉시 종료.

**동작**:
1. 시작 시 `~/.config/quota/quota-bar.json`에서 화면 설정(선택 항목) 로드
2. `~/.config/quota/config.json`을 `config.ResolveAccounts()`로 해석해 조회할 Claude 계정 목록 확정 (기본 `claude` + 유효한 추가 계정). quota-cli와 동일한 규칙·순서를 공유한다. skip된 항목은 로그로만 기록.
3. systray 아이콘 + 메뉴 구성 (계정별 그룹 + Codex)
4. 즉시 1회 refresh 실행, 이후 활동 기반 간격으로 자동 refresh
5. **refresh = 각 Claude 계정 `claude.GetQuotaForConfigDir(timeout, configDir)` (기본 계정은 `configDir=""`) + `codex.GetQuota()`를 병렬 호출** (내부 패키지)
6. 결과를 메뉴 항목에 표시

**계정 목록 확정 시점 (중요)**: systray는 런타임에 메뉴 항목을 추가·제거할 수 없다. 따라서 계정 수와 메뉴 레이아웃은 **onReady 시작 시점의 config로 고정**된다. `config.json`을 편집해 계정을 추가/제거하면 **quota-bar를 재시작**해야 반영된다.

**메뉴바 표시**:
- 아이콘 하나 + 선택된 항목들의 남은 % 표시. 상단 바 공간을 최소화한다.
- 선택 없음 시: 아이콘만 표시 (텍스트 없음)
- 단일 선택 시: 아이콘 + `95%`
- 복수 선택 시: 아이콘 + `95% 85%` (메뉴 순서대로 나열, 공백 구분). 라벨 없음 — 어떤 항목인지는 메뉴에서 체크 표시로 확인.

**메뉴 구성**:
- 각 항목은 체크박스이며, 체크된 항목이 상단 바에 표시된다.
- 항목마다 남은 % + 리셋까지 남은 시간을 한 줄로 표시한다.

```
── Claude ──
☐ Session 95% (4h 30m)
☐ Weekly 90% (2d 5h)
☐ Fable 80%
── Claude 2 ──
☐ Session 40% (3h 10m)
☐ Weekly 55% (1d 8h)
── Codex ──
☑ 5h 85% (2h 30m)
☐ Day 70% (18h 5m)
───────────────
Updated 14:30
Refresh
Start at Login
quota-bar v0.4.0
Quit
```

- 각 항목: `라벨 XX% (남은시간)` 형식. 남은시간 없으면 괄호 생략.
- 에러 발생 시 해당 계정/영역에 에러 메시지 표시 (계정별·Codex별 에러 행을 각각 둔다).
- 마지막 갱신 시간은 하단에 표시

**다중 Claude 계정 표시**:
- 추가 계정이 등록돼 있으면 기본 `Claude` 그룹 아래에 계정별 그룹을 순서대로 만든다. 그룹 헤더 라벨은 계정 key에서 파생한다: `claude`→`Claude`, `claude-2`→`Claude 2` (`ResolvedAccount.Label`).
- 각 그룹은 자기 `Session`/`Weekly` + 동적 extra 슬롯(아래 참조)을 가진다.
- Codex 그룹은 항상 마지막.

**메뉴 항목 키 스킴** (설정 파일 하위호환):
- 키 형식은 `<provider>_<suffix>`이며 `provider`는 계정 key(`claude`, `claude-2`, …) 또는 `codex`다. 계정 key는 `^claude-\d+$`라 `_`를 포함하지 않으므로, 첫 `_`가 항상 provider와 suffix를 가른다.
- 기본 계정 키는 **기존 그대로 유지**한다: `claude_session`, `claude_weekly_all`, `claude_extra_1`~`claude_extra_3`. 따라서 기존 `quota-bar.json`의 `selected` 값이 그대로 유효하다.
- 추가 계정은 동일 스킴을 계정 key로 확장한다: `claude-2_session`, `claude-2_weekly_all`, `claude-2_extra_1`~. Codex는 `codex_5h`, `codex_day` (그대로).
- 선택 상태(체크)는 슬롯 위치 기준 키로 저장되므로 모델명이 바뀌어도 유지된다.

**Claude 동적 슬롯 (extras)**:
- 각 Claude 계정 그룹의 `Session`/`Weekly`는 고정 항목, 그 아래에 계정별 동적 슬롯 3개(`<accountKey>_extra_1`~`<accountKey>_extra_3`, 기본 계정은 `claude_extra_N`)를 숨김 상태로 미리 만든다 (systray는 런타임 항목 제거 불가).
- `extras` 배열의 항목을 순서대로 슬롯에 채우고, 라벨은 화면에서 읽은 `label`을 그대로 쓴다. 데이터 없는 슬롯은 숨긴다.
- 설정 저장 키는 슬롯 위치 기준(`<accountKey>_extra_N`)이다 — 모델명이 바뀌어도 체크 선택이 유지된다.
- 계정마다 3개를 초과하는 extras는 표시하지 않는다.

**설정 파일**: `~/.config/quota/quota-bar.json`
```json
{ "selected": ["codex_5h", "claude_session"] }
```

- 마이그레이션: 로드 시 구 키 `claude_weekly_sonnet`이 있으면 `claude_extra_1`로 1회 치환 후 저장한다 (중복 제거 포함).

**에러 처리**:
- refresh 실패 시 이전 성공 데이터(lastOK)를 유지하고 에러 메시지만 표시
- 동시 refresh 방지 (mutex + running flag)

**Stale 데이터 경고**:
- provider별 마지막 성공 시각(`lastSuccessAt`)을 추적한다. provider는 각 Claude 계정 key(`claude`, `claude-2`, …)와 `codex`다.
- 해당 provider가 stale 임계 시간 이상 갱신 실패 시, 그 provider의 데이터에 `?` 접미사를 붙여 표시
  - 바 타이틀: `95%` → `95%?`
  - 메뉴 항목: `Session 95%` → `Session 95%?`
- "Updated" 행에 경과 시간 표시: `Updated 14:30 (claude 5m0s ago!)`
- carry/snapshot(실패 계정의 직전 성공값 유지, 성공 계정의 값 갱신)은 정확히 `<provider>_` prefix로 계정별로 분리된다. 계정 key에 `_`가 없어 `claude_`가 `claude-2_` 행을 잘못 매칭하지 않는다.

**로그**:
- 데몬 프로세스 로그 출력: `~/.config/quota/quota-bar.log`
- 에러, 시작/종료 이벤트 등을 기록

---

## Internal 패키지 사양

### internal/claude

**함수**:
- `GetQuota(timeout time.Duration) (map[string]any, error)` — 기본 계정 조회 (config-dir 미지정)
- `GetQuotaForConfigDir(timeout time.Duration, configDir string) (map[string]any, error)` — 지정한 `CLAUDE_CONFIG_DIR` 계정 조회. `configDir`가 빈 문자열이면 `GetQuota`와 동일.

- 내장 tmux 자동화로 Claude CLI에서 quota 조회. 두 함수는 동일한 조회 로직을 공유하며 config-dir 주입 여부만 다르다.

**tmux 자동화 흐름** (내장):
1. `tmux new-session -d -s <session> -x 120 -y 40 -c <safeDir> env -u CLAUDECODE -u ANTHROPIC_AUTH_TOKEN -u ANTHROPIC_BASE_URL [CLAUDE_CONFIG_DIR=<configDir>] claude`
   - `session` = `quota-{pid}` (config-dir 지정 시 계정별 고유 suffix를 붙여 동시 조회 세션 충돌 방지)
   - `safeDir` = `~/.config/quota` (Claude CLI가 CWD를 readdir할 때 macOS TCC 보호 폴더 접근 방지)
   - `CLAUDECODE` 제거: 중첩 세션 감지 회피
   - `ANTHROPIC_AUTH_TOKEN` / `ANTHROPIC_BASE_URL` 제거: 사용자 로그인 계정 quota를 읽도록 강제 (커스텀 엔드포인트/대체 토큰이 quota를 가로채지 않게)
   - `CLAUDE_CONFIG_DIR=<configDir>`: config-dir 지정 시에만 추가. **`-u` 옵션들 뒤, command 앞**에 둔다 (macOS `env`는 옵션이 `NAME=VALUE` 할당보다 먼저 와야 하므로 순서 고정). 명령줄에 직접 주입해야 tmux 서버 환경 상속과 무관하게 전달된다.
2. 스플래시(`Claude Code`) 등장까지 폴링 → Enter (초기 prompt 해제)
3. `/usage` 입력 → Enter
4. `% used` 와 `Esc to cancel` 이 모두 보일 때까지 폴링 (또는 `Error:` 검출 시 즉시 진행)
5. settle 대기 — `/usage` 화면 행이 비동기로 그려지므로 최소 2초 대기 후 500ms 간격으로 재캡처하며,
   연속 두 캡처가 동일해질 때까지 폴링한다 (최대 8초 상한, 애니메이션으로 인한 무한 대기 방지)
6. 마지막 캡처 결과를 파싱에 사용
7. ANSI 코드 제거 → `parseCaptured()` 로 파싱
8. Escape → `/exit` → Enter → `tmux kill-session` 클린업

**파싱 로직** (`parseCaptured`) — 줄 단위 파싱:
- `\d+% used` 를 포함한 줄을 모두 찾음 (진행 바 줄)
- 라벨 결정 (순서대로):
  1. 같은 줄에서 매치 앞부분의 바 문자(U+2580–U+259F)와 공백을 제거한 나머지 텍스트
  2. 없으면 위쪽으로 가장 가까운 비어있지 않은 줄 (최대 3줄, 바/Resets 줄이면 무효)
- 라벨 분류:
  - `Current session` 포함 → `session`
  - `all models` 포함 → `weeklyAll`
  - 그 외 → `extras` 항목. 이름은 `Current week (X)` 형식이면 `X`, 아니면 라벨 전체
- resets: 진행 바 줄 아래쪽으로 가장 가까운 비어있지 않은 줄이 `Resets ...` 형식이면 추출 (`toRelative`로 상대시간 정규화)
- 일부 행이 화면에 없거나 매칭이 일부만 되어도 매치된 항목만 반환 (부분 결과 허용)
- 하단 "What's contributing" 섹션의 `NN% of ...` 텍스트는 `% used` 패턴이 아니므로 매치되지 않는다

### internal/codex

**함수**: `GetQuota(timeout time.Duration) (map[string]any, error)`

**동작**:
1. `codex app-server` 프로세스를 시작 (stdin/stdout pipe)
2. JSON-RPC 2.0 프로토콜:
   - Request #1: `initialize` (clientInfo 전달)
   - Request #2: `account/rateLimits/read`
3. Response에서 `rateLimits` 또는 `rateLimitsByLimitId.codex` 추출
4. primary → fiveHour, secondary → day 매핑
5. `resetsAt` (epoch) → 상대 시간 문자열 변환 (`2h 30m`, `1d 3h` 등)
6. 프로세스 종료

### internal/render

**함수**: `Text(payload map[string]any) string`

- Claude, Codex 데이터를 사람이 읽을 수 있는 텍스트로 변환
- 리셋 시간이 있으면 `(남은시간, at 절대시각)` 형식으로 표시 (예: `(2h 30m, at 15:30)`)
- 24시간 이상이면 날짜 포함 (예: `(2d 5h, at Mar 6 15:30)`)
- 에러 있으면 Errors 섹션 추가
- 마지막에 `Generated: {RFC3339}` 타임스탬프

### internal/ui

**함수**: `GenIcon(pct int) []byte`

- 남은 퍼센트를 받아 22x22 PNG 아이콘 생성 (세로 바 레벨 표시, systray용)
- macOS template icon으로 사용 (`SetTemplateIcon`) — 다크모드/라이트모드에서 시스템이 자동으로 색상 반전

---

## 외부 의존성

| 패키지 | 용도 |
|--------|------|
| `github.com/getlantern/systray` | macOS systray (quota-bar 전용) |

시스템 의존성:
- `tmux`: Claude quota 조회 시 필요 (Claude CLI 자동화)
- `claude` CLI: Claude Code CLI (`~/.local/bin/claude` 또는 PATH)
- `codex` CLI: Codex CLI (PATH에 있어야 함)

---

## 현재 알려진 이슈

없음.
