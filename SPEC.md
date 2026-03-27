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
  "session":      { "used": 5, "left": 95, "spent": "$1.23/$5.00", "resetsIn": "4h 30m" },
  "weeklyAll":    { "used": 10, "left": 90, "spent": "$17.91/$50.00", "resetsIn": "2d 5h" },
  "weeklySonnet": { "used": 20, "left": 80 },
  "extra":        { "used": 0, "left": 100, "resetsIn": "18h 20m" }
}
```

각 항목:
- `used` (int): 사용 퍼센트 (0–100)
- `left` (int): 남은 퍼센트 (100 - used)
- `spent` (string, optional): 금액 표시 (예: `$17.91/$50.00`)
- `resetsIn` (string, optional): 리셋까지 남은 시간 (예: `4h 30m`, `2d 5h`)

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

**플래그**:
| 플래그 | 기본값 | 설명 |
|--------|--------|------|
| `--json` | false | JSON 포맷으로 출력 |
| `--timeout` | 40 | 타임아웃 (초) |

**동작**:
1. `claude.GetQuota(timeout)` 호출
2. `codex.GetQuota(timeout)` 호출
3. 둘 다 완료되면 결과를 JSON 또는 텍스트로 출력
4. 개별 provider 에러는 errors 배열에 포함, 프로세스 자체는 종료하지 않음

**JSON 출력 형식**:
```json
{
  "claude": { ... },
  "codex": { ... },
  "errors": []
}
```

### quota-bar

**용도**: macOS 메뉴바에 상주하며 quota를 주기적으로 갱신

실행 시 자동으로 백그라운드 프로세스로 전환된다 (`&` 불필요).
중복 실행 방지: `~/.config/quota/quota-bar.pid` 파일에 flock을 획득하여 단일 인스턴스만 실행된다. 이미 실행 중이면 즉시 종료.

**동작**:
1. 시작 시 `~/.config/quota/quota-bar.json`에서 설정 로드
2. systray 아이콘 + 메뉴 구성
3. 즉시 1회 refresh 실행, 이후 60초 간격 자동 refresh
4. **refresh = `claude.GetQuota()` + `codex.GetQuota()` 직접 호출** (내부 패키지)
5. 결과를 메뉴 항목에 표시

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
☐ Sonnet 80%
☐ Extra 100% (18h 20m)
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
- 에러 발생 시 해당 LLM 영역에 에러 메시지 표시
- 마지막 갱신 시간은 하단에 표시

**설정 파일**: `~/.config/quota/quota-bar.json`
```json
{ "selected": ["codex_5h", "claude_session"] }
```

**에러 처리**:
- refresh 실패 시 이전 성공 데이터(lastOK)를 유지하고 에러 메시지만 표시
- 동시 refresh 방지 (mutex + running flag)

**Stale 데이터 경고**:
- provider별 마지막 성공 시각(`lastSuccessAt`)을 추적
- 5분 이상 갱신 실패 시, 해당 provider의 데이터에 `?` 접미사를 붙여 표시
  - 바 타이틀: `95%` → `95%?`
  - 메뉴 항목: `Session 95%` → `Session 95%?`
- "Updated" 행에 경과 시간 표시: `Updated 14:30 (claude 5m0s ago!)`

**로그**:
- 데몬 프로세스 로그 출력: `~/.config/quota/quota-bar.log`
- 에러, 시작/종료 이벤트 등을 기록

---

## Internal 패키지 사양

### internal/claude

**함수**: `GetQuota(timeout time.Duration) (map[string]any, error)`

- 내장 tmux 자동화로 Claude CLI에서 quota 조회

**tmux 자동화 흐름** (내장):
1. `tmux new-session -d -s quota-{pid} -x 120 -y 40 claude`
2. 6초 대기 → Enter (trust dialog 해제)
3. 4초 대기 → `/status` 입력 → 2초 대기 → Enter (autocomplete 선택)
4. 5초 대기 → Tab (Status→Config) → 2초 대기 → Tab (Config→Usage)
5. 7초 대기 → `tmux capture-pane -t SESSION -p`로 캡처
6. ANSI 코드 제거 → `parseCaptured()` 로 파싱
7. Escape → `/exit` → Enter → `tmux kill-session` 클린업

**파싱 로직** (`parseCaptured`):
- `\d+% used` 패턴을 모두 찾음
- 각 매치에서 200자 이전 텍스트에서 가장 가까운 라벨 매칭:
  - "Current session" → session
  - "all models" → weeklyAll
  - "Sonnet only" → weeklySonnet
  - "Extra usage" → extra
- 매치 이후 200자에서 spent, resets 정보 추출

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
