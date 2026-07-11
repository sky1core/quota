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
  "session":   { "used": 5, "left": 95, "resetsIn": "4h 30m", "resetsAt": "2026-07-06T15:04:00+09:00" },
  "weeklyAll": { "used": 10, "left": 90, "resetsIn": "2d 5h", "resetsAt": "2026-07-08T11:59:00+09:00" },
  "extras": [
    { "label": "Fable", "used": 20, "left": 80, "resetsIn": "2d 5h", "resetsAt": "2026-07-08T11:59:00+09:00" }
  ]
}
```

각 항목:
- `used` (int): 사용 퍼센트 (0–100)
- `left` (int): 남은 퍼센트 (100 - used)
- `resetsIn` (string, optional): 리셋까지 남은 시간 (예: `4h 30m`, `2d 5h`)
- `resetsAt` (`time.Time`, optional): 정확한 절대 리셋 시각. `/usage`의 `Resets` 절대표기를 파싱해 보존한다. 절대표기를 파싱할 수 없으면(이미 상대표기 등) 생략한다. `--json`에서는 RFC3339 문자열로 직렬화된다.

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
  "fiveHour": { "left": 85, "resetsIn": "2h 30m", "resetsAt": "2026-07-06T15:04:00+09:00" },
  "day":      { "left": 70, "resetsIn": "18h 5m", "resetsAt": "2026-07-07T09:00:00+09:00" },
  "credits":  { "balance": "100.00", "hasCredits": true, "unlimited": false },
  "planType": "pro",
  "resetCredits": {
    "available": 2,
    "items": [
      { "title": "Full reset (Weekly + 5 hr)", "expiresIn": "1d 0h", "expiresAt": "2026-07-12T10:42:00+09:00" },
      { "title": "Full reset (Weekly + 5 hr)", "expiresIn": "6d 23h", "expiresAt": "2026-07-18T09:33:00+09:00" }
    ]
  }
}
```

각 항목:
- `fiveHour`: 5시간 윈도우 rate limit
- `day`: 일일 윈도우 rate limit
- `left` (int): 남은 퍼센트 (100 - usedPercent)
- `resetsIn` (string): 리셋까지 남은 시간 (예: `2h 30m`, `45m`, `1d 3h`)
- `resetsAt` (`time.Time`, optional): 정확한 절대 리셋 시각. 응답의 `resetsAt`(epoch)를 그대로 보존한다. epoch가 없으면 생략한다.
- `resetsIn`/`resetsAt`는 **윈도우가 언제 리셋되는지**다. 아래 `resetCredits`(초기화권)의 `expiresIn`/`expiresAt`(초기화권이 언제 만료돼 사라지는지)와는 다른 축이다.

**초기화권 (`resetCredits`, optional)**: Codex가 부여하는 일회성 rate-limit 리셋 grant(응답의 top-level `rateLimitResetCredits`). rate limit 윈도우와 별개이며 각 grant마다 **자체 만료 시각**이 있다.
- `available` (int): 실제로 나열한 사용 가능(status `available`) 초기화권 수. **항상 `len(items)`와 같다** — 응답의 `availableCount`는 status 필터와 독립 소스라 어긋날 수 있어 신뢰하지 않는다(카운트가 목록과 모순되지 않게).
- `items` (`[]map[string]any`): status가 `available`인 초기화권만, **만료 임박순**(오름차순)으로 정렬한다. 각 항목:
  - `title` (string): grant 제목 (예: `Full reset (Weekly + 5 hr)`)
  - `expiresIn` (string, optional): 만료까지 남은 시간 (예: `1d 0h`, `6d 23h`). 이미 지났으면 `0m`.
  - `expiresAt` (`time.Time`, optional): 정확한 절대 만료 시각. 응답의 `expiresAt`(epoch)를 그대로 보존한다. `--json`에서는 RFC3339 문자열로 직렬화된다.
  - `expiresIn`/`expiresAt`는 응답의 같은 `expiresAt`(epoch)에서 파생되므로 **함께 존재하거나 함께 생략**된다. epoch가 없는 grant는 둘 다 생략하고 `title`만 남는다.
- 사용 가능한 grant가 하나도 없으면(items 비면) `resetCredits` 키 자체를 생략한다.

**공통**: 리셋 시간은 `resetsIn` 키로 **남은 시간**(상대시간)을 반환한다. 정확한 절대 리셋 시각을 아는 경우(Claude: `/usage`의 `Resets` 절대표기, Codex: 응답의 `resetsAt` epoch) `resetsAt` 키로 절대 시각을 **함께** 반환할 수 있다(선택). `resetsIn`을 절대시간 문자열로 대체하지 않는다 — 상대·절대는 서로 다른 키로 공존한다.

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
4. 즉시 1회 refresh 실행, 이후 활동 기반 간격으로 자동 refresh (활성/idle 주기는 `quota-bar.json`으로 설정 가능, 기본 3분/30분)
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
   Reset credits: 2  (1d 0h)  ▸
                              ├ 1d 0h
                              └ 6d 23h
───────────────
Updated 14:30
Reset as clock time
Refresh
Start at Login
quota-bar v0.4.0
Quit
```

- 각 항목: `라벨 XX% (남은시간)` 형식. 남은시간 없으면 괄호 생략.
- `Reset as clock time` 체크 시 각 항목의 괄호를 남은시간 대신 절대 시각으로 표시한다: `Weekly 90% (Jul 6 15:04)`. 절대 시각을 모르는 항목(`resetsAt` 없음)은 남은시간으로 유지한다.

**Codex 초기화권 (Reset credits 행)**:
- 라벨은 `Reset credits`다 — Codex 공식 표현(응답 필드 `rateLimitResetCredits`)을 따르며 "초기화권"(초기화=reset, 권=credit) 의미를 담는다.
- Codex `resetCredits`(초기화권)가 있으면 Codex 섹션에 `Reset credits: N` 부모 행을 두고, 각 초기화권 만료를 **서브메뉴**로 나열한다(만료 임박순). `N`은 사용 가능 수(`= 나열한 자식 수`), 부모의 괄호는 가장 임박한 만료다(수식어 없이 시각만).
- **표시 전용**이다: 체크박스 아님, 상단 % 바에 넣지 않는다. 부모·자식 모두 enable 상태로 두어(가독성 위해 — disable 회색 텍스트를 피한다) 클릭은 아무 동작 없이 무시된다(정보 행). 부모 행에는 툴팁을 달지 않는다(뷰를 가림).
- 부모/자식 시각도 **`Reset as clock time` 토글을 그대로 따른다** — off면 남은시간(`1d 0h`), on이면 절대 시각(`Jul 12 10:42`). 절대 시각을 모르면 남은시간으로 유지하고, 남은시간·절대 시각 모두 없으면 grant 제목으로 대체한다.
- 자식 슬롯은 고정 개수(`resetCreditSlots`)를 미리 만들어 두고 데이터 수만큼 show/hide 한다(systray 런타임 항목 추가 불가). 사용 가능 초기화권이 슬롯 수를 초과하면 부모 카운트는 실제 수를 표시하고 서브메뉴는 임박한 슬롯 수만 보여준다. 사용 가능 초기화권이 없으면 부모·자식 모두 숨긴다.
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
{ "selected": ["codex_5h", "claude_session"], "showResetTime": false, "refreshActiveMinutes": 30 }
```

- `selected` (string 배열): 상단 바에 표시할 체크된 항목 키.
- `showResetTime` (bool, optional, 기본 false): true면 각 항목의 리셋을 남은시간 대신 절대 시각(`FormatResetAt`, 예: `Jul 6 15:04`)으로 표시. Codex 초기화권(Reset credits 부모·서브메뉴)의 만료 시각도 동일하게 따른다. 메뉴의 `Reset as clock time` 체크로 토글하며, 즉시 저장 후 재조회 없이 메뉴만 다시 그린다(초기화권 행 포함). 상단 바(퍼센트 전용)에는 영향 없다.
- `refreshActiveMinutes` (int, optional): 활성 상태 refresh 주기(분). **없거나 0 이하면 앱 기본값 3분**을 쓴다(명시 기본값 — 암시 fallback 아님). 값이 있으면 그 값을 적용한다.
- `refreshIdleMinutes` (int, optional): idle 상태 refresh 주기(분). **없거나 0 이하면 앱 기본값 30분**. stale 경고 임계는 두 주기(active/idle) 중 **큰 값 + 5분**으로 계산해, 어느 주기를 늘려도(활성 주기를 idle보다 크게 잡아도) 정상 갱신을 stale로 오판하지 않는다.
  - 두 주기는 **메뉴에 토글이 없다**. 파일로만 지정하며, 변경은 계정 목록과 마찬가지로 **quota-bar 재시작 후 반영**된다(시작 시 고정).
  - Codex 초기화권은 rate limit 응답에 함께 오므로 별도 주기가 없다. 이 주기는 codex/claude 조회 전체에 적용된다.
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
- resets: 진행 바 줄 아래쪽으로 가장 가까운 비어있지 않은 줄이 `Resets ...` 형식이면 추출. 상대시간(`resetsIn`)으로 정규화하고, 절대표기를 파싱할 수 있으면 절대 리셋 시각(`resetsAt`, `time.Time`)도 함께 채운다 (`parseReset`)
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
5. `resetsAt` (epoch) → 상대 시간 문자열(`resetsIn`: `2h 30m`, `1d 3h` 등) + 절대 시각(`resetsAt`: `time.Time`) 함께 반환
6. top-level `rateLimitResetCredits`(초기화권) → status `available`인 grant만 만료 임박순으로 `resetCredits`(`available` + `items`)로 반환. 사용 가능한 grant가 없으면 키 생략.
7. 프로세스 종료

### internal/render

**함수**:
- `Text(payload map[string]any) string`
- `FormatResetAt(t time.Time) string` — 절대 리셋 시각을 `Jan 2 15:04`(로컬 tz, 24시간제, **날짜 항상 포함**, 연도 생략)로 포맷. quota-bar와 공유하는 단일 포맷 소스.

- Claude, Codex 데이터를 사람이 읽을 수 있는 텍스트로 변환
- 리셋 시간이 있으면 `(남은시간, at 절대시각)` 형식으로 표시 (예: `(2d 5h, at Jul 6 15:04)`)
- Codex `resetCredits`(초기화권)가 있으면 Codex 섹션에 `Reset credits: N  (expires 절대시각)` 요약 줄을 추가한다. `N`은 사용 가능 수, 절대시각은 가장 임박한 만료(`items[0]`). 전체 목록은 `--json`으로 확인한다.
- 절대시각은 항목에 `resetsAt`가 있으면 그 값을 `FormatResetAt`로 포맷한다(역산 없이 정확).
- `resetsAt`가 없으면 `resetsIn`을 현재시각 기준으로 역산한다(`endTime`, 하위호환 fallback — 24시간 미만은 `15:04`, 이상은 `Jan 2 15:04`).
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
