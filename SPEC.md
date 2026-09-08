# quota — Claude Code & Codex CLI Quota Monitor

## 개요

Go로 작성된 Claude Code와 Codex CLI의 사용량(quota) 조회 도구.
두 개의 독립적인 바이너리를 제공한다:

| 바이너리 | 설명 |
|----------|------|
| `quota-cli` | quota 조회, 비대화형 Claude/Codex 실행 위임, agent hook 정책 관리 |
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
│   ├── agenthooks/          # Claude/Codex 공통 agent hook 정책 평가
│   ├── agentoverlay/        # spec 파일 기반 Claude/Codex 지침 오버레이 설치·검증
│   ├── quotacache/          # 계정별 조회 결과 공유 캐시
│   ├── render/render.go     # 텍스트 출력 포맷터
│   └── ui/icon.go           # systray 아이콘 (22x22 PNG)
├── go.mod
├── go.sum
└── SPEC.md
```

---

## 데이터 모델

### 공통: 자기서술 윈도우 목록 (모든 provider)

**어떤 provider든** 계정 하나의 quota를 같은 모양으로 반환한다:

```json
{ "windows": [ { "key": "…", "label": "…", "left": 95, "resetsIn": "…", "resetsAt": "…" }, … ] }
```

불변식 — **라벨은 생산자가 만들고, 소비자는 표시만 한다:**

- `key` (string, 필수): 그 창의 **슬롯/선택 주소**. 화면에 절대 나오지 않는다.
- `label` (string, 필수): 표시명. **그 provider가 자기 진실 소스에서 도출**한다 — Codex는 `windowDurationMins`, Claude는 `/usage` 화면 텍스트. **범위를 뭉개거나 단어를 지어내지 않는다**(5시간이 아닌 창을 `5h`라 부르지 않는다).
- `left` (int): 남은 퍼센트. `resetsIn`/`resetsAt`은 아래 공통 규칙을 따른다.
- provider 고유 필드는 병존 가능(Codex `windowMins`, Claude `used`).

**계정 단위 진단**: `windowErrors` (string 배열, optional)는 `windows`와 같은 레벨에 두며, 응답에 있는 집계 quota 창의 사용량·기간을 유효하게 읽지 못한 경우를 알린다(Claude `session`/`weekly_all`, Codex 모든 창). 정상 창은 계속 반환하지만, 이 진단이 있으면 신규 작업 배정에서 제외한다. 응답에 창 자체가 없는 경우는 오류가 아니다.

**데이터는 절대 잃지 않는다 (핵심)**: 목록에는 응답에 **실재하는 창이 전부** 들어간다. 같은 창(동일 기간/동일 행)의 중복만 제거한다. **소비자의 표시 제약(systray는 런타임에 행을 못 만들어 슬롯이 유한하다)을 생산자 안으로 밀어넣지 않는다** — 그러면 슬롯 제약이 없는 `quota-cli`까지 진짜 창을 잃는다.
- 그래서 `key`는 **유일하지 않을 수 있다**(예: 서로 다른 두 짧은 창이 같은 슬롯 주소를 가질 수 있다). 그건 **슬롯이 유한한 소비자가 해결한다**(첫 창이 슬롯 차지, 나머지는 그 소비자에서만 안 보임). 데이터에는 둘 다 남는다.
- `WindowKeys()`는 **슬롯을 미리 만들어야 하는 소비자를 위한 힌트**일 뿐 데이터의 상한이 아니다. 목록에는 그 어휘 밖의 key(예: `extra_4`)가 올 수 있고, 슬롯이 유한한 소비자는 그런 key를 무시한다. `quota-cli`는 전부 표시한다.

**소비자 계약**: `render`/`quota-bar`/외부 소비자는 **목록을 순회해 `label`을 그대로 출력**한다. 어떤 창 이름도, 창 종류 목록도 갖지 않는다. 그래서 provider가 창을 없애거나 되살리거나 기간을 바꿔도(예: Codex 5h 소멸→재등장, 주간→월간 교체, Claude `week`→`5 days`) **소비자 코드 변경이 0이다.** 응답에 있는 창만 목록에 들어가고, 없는 창은 항목 자체가 없다(소비자는 그 행을 숨긴다).

### Claude quota

`internal/claude`가 반환하는 `map[string]any` — 위 공통 계약을 따른다:

```json
{
  "windows": [
    { "key": "session",    "label": "Session", "used": 5,  "left": 95, "resetsIn": "4h 30m", "resetsAt": "2026-07-06T15:04:00+09:00" },
    { "key": "weekly_all", "label": "Week",    "used": 10, "left": 90, "resetsIn": "2d 5h",  "resetsAt": "2026-07-08T11:59:00+09:00" },
    { "key": "extra_1",    "label": "Fable",   "used": 20, "left": 80, "resetsIn": "2d 5h",  "resetsAt": "2026-07-08T11:59:00+09:00" }
  ]
}
```

- **키**: `session`/`weekly_all`은 `/usage`의 두 집계 행, `extra_N`은 모델별 행(모델 세대마다 이름이 바뀌므로 위치 슬롯). 화면 순서를 유지한다. 파서는 화면의 **모든 모델 행**을 낸다(`extra_4` 이상도) — `WindowKeys()`가 노출하는 `extra_1`~`extra_3`은 슬롯이 유한한 소비자용 힌트일 뿐이다.
- **라벨 도출** (`windowLabel`): Claude는 창 기간 필드를 주지 않으므로 **`/usage` 화면 텍스트가 유일한 진실**이다. `Current ` 접두를 떼고, 모델 괄호가 있으면 모델명을 쓴다:
  - `Current session` → `Session`
  - `Current week (all models)` → `Week`
  - `Current week (Fable)` → `Fable`
  - `Current 5 days (all models)` → `5 days` ← **기간이 바뀌면 라벨이 따라간다**
  - 알 수 없는 문구는 **그대로 통과**시킨다(이름을 지어내지 않는다).
- `used` (int): 사용 퍼센트 (Claude 고유 필드, 0–100)
- `resetsAt`: `/usage`의 `Resets` 절대표기를 파싱해 보존. 파싱 불가 시 생략.
- 같은 라벨의 모델 행이 중복되면 첫 항목만 채택한다(개수 상한은 두지 않는다).

**계정 단위**: `internal/claude`가 반환하는 이 구조는 Claude 계정 **하나**의 quota다.
계정은 `CLAUDE_CONFIG_DIR`(Claude CLI config 디렉터리)로 구분된다 — config-dir이 다르면 다른 계정이다.
config-dir 미지정 시 claude CLI 기본 계정(`~/.claude` 또는 프로세스의 `CLAUDE_CONFIG_DIR`)을 조회한다.
여러 계정을 합쳐 출력하는 것은 `quota-cli`의 책임이며, `internal/claude`는 계정 분리 메커니즘을 알지 못한다.

### Codex quota

`internal/codex` 패키지가 반환하는 `map[string]any`:

```json
{
  "windows": [
    { "key": "5h",     "label": "5h", "windowMins": 300,   "left": 85, "resetsIn": "2h 30m", "resetsAt": "2026-07-06T15:04:00+09:00" },
    { "key": "weekly", "label": "7d", "windowMins": 10080, "left": 70, "resetsIn": "6d 19h", "resetsAt": "2026-07-18T09:00:00+09:00" }
  ],
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

**윈도우 (`windows`)**: 위 **공통 자기서술 목록 계약**을 따른다. Codex 응답의 rate-limit 윈도우는
`primary`/`secondary` 위치로 오지만, Codex는 **어떤 윈도우를 어느 위치에 실을지, 어떤 윈도우가
존재하는지 시점에 따라 바꾼다**(실측: 모델 출시 전후로 5h가 사라지고 주간이 primary로 이동했고, 이후
주간마저 월간(43200분)으로 바뀌었다). 그래서 위치도 범위 버킷도 아닌, 각 항목이 자기 사실을 들고
다니는 목록이다.

Codex 고유 사항:
- `windowMins` (int): 응답의 `windowDurationMins` 원본(분). 이 창이 무엇인지의 **사실**이다.
- `label`: **`windowMins`에서 진실하게** 만든다(`windowLabel`) — 300→`5h`, 600→`10h`, 1440→`1d`, 10080→`7d`, 43200→`30d`, 90→`1h 30m`. 5시간이 아닌 창을 `5h`라 부르지 않는다.
- `key`: 기간→슬롯 버킷(`windowKey`), 어휘는 `WindowKeys()` = `5h`(≤12h) / `daily`(≤2d) / `weekly`(≤14d) / `monthly`(>14d). **슬롯 식별자일 뿐 화면에 안 나온다.**
- 목록 순서는 **실제 기간 오름차순**(짧은 창 먼저) — 응답 위치와 무관.
- 중복 제거는 **동일 `windowMins`**(같은 창)만 대상이다. 기간이 다르면 다른 창이므로 같은 슬롯 key를 갖더라도 **둘 다 낸다**(슬롯 충돌은 소비자가 해결).
- `windowDurationMins`가 없는 윈도우는 진실한 라벨을 만들 수 없어 **생략**한다(위치/버킷 라벨링이 이 설계로 제거하려는 버그다). 표시 가능한 윈도우가 없으면 `windows` 키를 생략한다.
- `resetsIn`/`resetsAt`는 **윈도우가 언제 리셋되는지**다. 아래 `resetCredits`(초기화권)의 `expiresIn`/`expiresAt`(초기화권이 언제 만료돼 사라지는지)와는 다른 축이다.

**초기화권 (`resetCredits`, optional)**: Codex가 부여하는 일회성 rate-limit 리셋 grant(응답의 top-level `rateLimitResetCredits`). rate limit 윈도우와 별개이며 각 grant마다 **자체 만료 시각**이 있다.
- `available` (int): 실제로 나열한 사용 가능(status `available`) 초기화권 수. **항상 `len(items)`와 같다** — 응답의 `availableCount`는 status 필터와 독립 소스라 어긋날 수 있어 신뢰하지 않는다(카운트가 목록과 모순되지 않게).
- `items` (`[]map[string]any`): status가 `available`인 초기화권만, **만료 임박순**(오름차순)으로 정렬한다. 각 항목:
  - `title` (string): grant 제목 (예: `Full reset (Weekly + 5 hr)`)
  - `expiresIn` (string, optional): 만료까지 남은 시간 (예: `1d 0h`, `6d 23h`). 이미 지났으면 `0m`.
  - `expiresAt` (`time.Time`, optional): 정확한 절대 만료 시각. 응답의 `expiresAt`(epoch)를 그대로 보존한다. `--json`에서는 RFC3339 문자열로 직렬화된다.
  - `expiresIn`/`expiresAt`는 응답의 같은 `expiresAt`(epoch)에서 파생되므로 **함께 존재하거나 함께 생략**된다. epoch가 없는 grant는 둘 다 생략하고 `title`만 남는다.
- 사용 가능한 grant가 하나도 없으면(items 비면) `resetCredits` 키 자체를 생략한다.

**계정 단위**: `internal/codex`가 반환하는 이 구조는 Codex 계정 **하나**의 quota다.
계정은 `CODEX_HOME`(Codex CLI home 디렉터리)로 구분된다 — home이 다르면 다른 계정이다.
home 미지정 시 codex CLI 기본 계정(`~/.codex` 또는 프로세스의 `CODEX_HOME`)을 조회한다.
같은 과금 계정을 서로 다른 `CODEX_HOME`에 각각 로그인해 쓸 수도 있으나, 사용량 한도·초기화권은
서버측 계정 단위라 같은 계정이면 home이 달라도 동일하게 나온다(격리되는 것은 로컬 설정·세션뿐).
여러 계정을 합쳐 출력하는 것은 `quota-cli`/`quota-bar`의 책임이며, `internal/codex`는 계정 분리
메커니즘을 알지 못한다.

**공통**: 리셋 시간은 `resetsIn` 키로 **남은 시간**(상대시간)을 반환한다. 정확한 절대 리셋 시각을 아는 경우(Claude: `/usage`의 `Resets` 절대표기, Codex: 응답의 `resetsAt` epoch) `resetsAt` 키로 절대 시각을 **함께** 반환할 수 있다(선택). `resetsIn`을 절대시간 문자열로 대체하지 않는다 — 상대·절대는 서로 다른 키로 공존한다.

---

## 바이너리 사양

### quota-cli

**용도**: 터미널에서 quota를 조회하거나, quota에 따라 계정을 선택해 Claude/Codex를 비대화형으로 실행한다. 계정별 결과는 공유 캐시(§공유 캐시)로 기본 60초 재사용한다.

**지원 OS**: macOS, Linux. Windows는 지원하지 않는다.

**플래그** (조회 모드):
| 플래그 | 기본값 | 설명 |
|--------|--------|------|
| `--json` | false | JSON 포맷으로 출력 |
| `--timeout` | 40 | 타임아웃 (초) |

**서브커맨드** (`account`): 추가 Claude/Codex 계정을 `config.json`에 관리한다. 손으로 파일을 편집하지 않아도 된다.
| 명령 | 설명 |
|------|------|
| `quota-cli account list` | 등록된 계정 목록과 config 경로 출력 (Claude/Codex 그룹, 각 기본 계정 포함) |
| `quota-cli account add <key> <dir>` | 계정 추가. **`key` 접두사로 provider를 판별한다**: `claude-<N>`이면 Claude(`dir`=`CLAUDE_CONFIG_DIR`), `codex-<N>`이면 Codex(`dir`=`CODEX_HOME`). 그 외 key는 거부. `dir`는 `~` 확장 지원. 검증(형식·중복 key·중복 dir)을 통과해야 저장하며, `dir`가 없으면 경고만 하고 진행한다. `dir`는 유저가 쓴 그대로 저장한다. |
| `quota-cli account rm <key>` | 계정 제거. `codex-<N>`이면 Codex 목록에서, 그 외는 Claude 목록에서 제거한다. 같은 key의 `execPrompt.accountSettings`도 함께 제거한다. |

**서브커맨드 (`update`) — 수동 업데이트**: `quota-cli update`는 Go module proxy가 해석한 `@latest` 릴리스 태그(`internal/update.Latest`)를 현재 바이너리 버전과 비교해, 같으면 "이미 최신"을 출력하고, 다르면 `go install <module>/cmd/quota-cli@<latest>`로 설치한 뒤 설치 경로와 버전을 출력한다.
- **수동 전용**: 어떤 조회 경로도 업데이트를 부수 효과로 일으키지 않는다. quota-cli의 update는 quota-bar를 건드리지 않는다(역도 같다). (조회 결과 공유 캐시(§공유 캐시)는 이와 별개 채널이다.)
- 로컬 빌드에서 실행하면 항상 최신 릴리스로 교체된다: 로컬 빌드의 버전은 릴리스 태그와 일치하지 않는 형태(`v0.9.0+dirty`, 커밋 해시, `dev` 등)라 비교가 불일치한다 — 의도된 동작이며, 최신 태그보다 앞선 dev 빌드라면 사실상 다운그레이드가 되지만 `업데이트: X → Y` 출력에 그대로 드러난다.
- 요구사항: PATH에 `go` 필요. 제약: 방금 push한 태그는 proxy 캐시로 몇 분 늦게 보일 수 있다.

**서브커맨드 (비대화형 프롬프트 실행/agent 선택)**:
| 명령 | 실행 대상 |
|------|-----------|
| `quota-cli exec-prompt --agent=claude [args...]` | 선택된 Claude 계정으로 `claude -p [args...]` |
| `quota-cli exec-prompt --agent=codex [args...]` | 선택된 Codex 계정으로 `codex exec [args...]` |
| `quota-cli select-agent [--agent=all\|claude\|codex] [--json]` | 선택된 provider/account와 실행 prefix 출력 |
| `quota-cli select-agent --agent=claude --model=<model>` | Claude 모델 row를 반영한 Claude 계정 선택 |

**서브커맨드 (`agent hooks`) — Claude/Codex agent hook 정책 관리**:
| 명령 | 설명 |
|------|------|
| `quota-cli agent hooks init --preset=github-history-guard [--force]` | 기본 정책 파일을 `~/.config/quota/agent-hooks.d/` 아래에 생성 |
| `quota-cli agent hooks list [--json]` | 정책 파일 목록과 rule/test 개수 출력 |
| `quota-cli agent hooks plan [--runtime=all\|claude\|codex] [--binary <path>] [--json]` | Claude/Codex hook 설치 위치와 evaluator 명령 출력, 파일은 수정하지 않음 |
| `quota-cli agent hooks apply [--runtime=all\|claude\|codex] [--binary <path>]` | 현재 사용자 계정의 Claude/Codex hook 설정 파일을 백업 후 evaluator hook 설치 |
| `quota-cli agent hooks verify [--json]` | 정책 파일 검증과 내장 positive/negative test 실행 |
| `quota-cli agent hooks doctor [--runtime=all\|claude\|codex] [--binary <path>] [--json]` | Claude/Codex 양쪽 hook 설정에 evaluator hook이 설치되어 있는지 확인 |
| `quota-cli agent hooks eval [--runtime=claude\|codex] [--command <shell-command>]` | hook에서 호출되는 내부 evaluator. 차단 시 exit code 2 |

**서브커맨드 (`agent overlay`) — spec 파일 기반 Claude/Codex 지침 오버레이 설치·검증**: 공통 플래그는 `--spec <file>`, `--json`이고 종료 코드는 usage 2, 실패 1, 성공 0이다.
| 명령 | 설명 |
|------|------|
| `quota-cli agent overlay init [--spec <file>] [--force]` | placeholder 명령이 든 spec 템플릿 생성. 기존 파일은 `--force` 없이 거부 |
| `quota-cli agent overlay plan [--spec <file>] [--runtime=all\|claude\|codex]` | 대상 파일 경로와 이벤트별 상태(Claude present/missing/stale, Codex present/missing/mismatch) 출력, 파일은 수정하지 않음 |
| `quota-cli agent overlay apply [--spec <file>] [--runtime=all\|claude\|codex]` | Claude settings에 spec 엔트리를 백업 후 설치. Codex apply는 미지원 |
| `quota-cli agent overlay doctor [--spec <file>] [--runtime=all\|claude\|codex]` | runtime별 상태(installed/degraded/unconfigured/error) 보고. verify 명령은 실행하지 않으며 최고 상태는 installed |
| `quota-cli agent overlay verify [--spec <file>]` | 엔트리 검사 + 런타임별 verify 명령 실행. 모든 configured 런타임이 enforced일 때만 exit 0 |

**`exec-prompt` 동작**:
- `--agent`는 필수이며 `claude`/`codex`만 허용한다. 그 뒤 `args`는 순서와 값을 바꾸지 않고 고정 접두(`claude -p`/`codex exec`) 뒤에 전달한다. stdin/stdout/stderr와 최종 종료 상태도 원본 CLI가 직접 담당하며, quota-cli는 선택 결과나 중간 데이터를 출력 스트림에 섞지 않는다.
- 선택할 provider의 등록 계정만 60초 캐시 기준으로 병렬 조회한다. 조회 실패 계정과 현재 적용되는 quota 창이 계정별 `minLeftPct` 미만인 계정은 후보에서 제외하며, 후보가 없으면 원본 CLI를 실행하지 않고 실패한다.
- 신규 작업은 응답에 5시간 quota 창이 있을 때 그 잔여량이 25% 이상이어야 배정한다. Claude는 `session`, Codex는 실제 `windowMins == 300`인 창으로 판정한다. 정상 응답에 주간 창만 있으면 그 창의 `minLeftPct` 기준으로 판단한다. 조회·파싱 오류나 `windowErrors`가 있거나, 적용할 창의 잔여량을 유효한 0~100% 수치로 읽을 수 없거나, 적용할 창이 하나도 없으면 제외한다. 응답에 없는 제한을 계정 종류로 추정하거나 추가하지 않는다. 이 진입 기준은 계정별 보존분 `minLeftPct`(기본 5%)와 별개로 적용하며 선택 점수에서 차감하지 않는다.
- 장기 창을 최우선, 짧은 창을 다음 순서로 비교한다. 비교값은 `남은 % - minLeftPct`이며, 양쪽 모두 리셋 시각을 알면 `비교값 / 리셋까지 남은 분`이 큰 쪽을 우선해, 같은 비교값이면 먼저 리셋되는 계정을 먼저 소비한다. 리셋 시각을 모르는 쪽이 있으면 비교값으로 비교한다. 모든 비교값이 같으면 config 순서가 빠른 계정을 선택한다.
- Codex는 실제 `windowMins`가 가장 큰 창을 장기 기준, 가장 작은 창을 짧은 기준으로 사용한다.
- Claude는 기본적으로 `weekly_all` 다음 `session` 순서로 비교한다. `--model`/`-m`이 지정돼도 요청 모델값과 실제 추가 quota row label이 맞을 때만 그 row를 본다. Opus처럼 전용 row가 없는 모델은 별도 quota를 가정하지 않고 `weekly_all`, `session`으로 비교한다. Fable처럼 해당 모델 창의 남은 비율을 읽을 수 있으면 그 계정에는 해당 모델 창의 계정별 `minLeftPct` 하한선을 적용한다. 살아남은 모든 후보가 남은 비율을 읽을 수 있는 해당 모델 창을 갖고 있을 때만 해당 모델 창을 우선 비교하고, 일부 후보에만 있으면 `weekly_all`, `session`으로 비교한다.
- 선택된 추가 Claude 계정은 `CLAUDE_CONFIG_DIR`, 추가 Codex 계정은 `CODEX_HOME`으로 실행한다. 기본 계정은 상속된 해당 변수를 유지한다. 조회한 로그인 계정과 실행 계정이 달라지지 않도록 Claude는 `ANTHROPIC_*`/`CLAUDE_*`의 인증·엔드포인트 override와 `CLAUDECODE`를, Codex는 `CODEX_*`/`OPENAI_*`의 인증·엔드포인트 override를 제거한다.
- 대화형 Claude/Codex 실행은 지원하지 않는다.

**`select-agent` 동작**:
- 사용자의 프롬프트를 실행하지 않는다. stdout에는 선택된 provider/account, 실행 prefix, 추가 계정에 필요한 환경 변수, 현재 셸에서 제거해야 할 override 환경 변수 이름, 후보별 quota 창 요약을 출력한다.
- 기본 `--agent=all`은 Claude/Codex configured 계정을 모두 비교한다. `--agent=claude` 또는 `--agent=codex`는 해당 provider 안에서만 선택한다.
- quota 조회는 `exec-prompt`와 같이 60초 공유 캐시를 우선 사용한다. 5시간 잔여량 25% 진입 기준과 `execPrompt.accountSettings.<key>.minLeftPct`도 동일하게 적용한다.
- 통합 모드에서는 provider 공통 장기/단기 창만 비교하고 Claude 모델별 extra row는 보지 않는다. `--model`은 `--agent=claude`에서만 허용한다.
- 후보가 없으면 후보별 실패/제외 이유를 출력한 뒤 non-zero로 종료한다. JSON 모드는 같은 정보를 `selected`, `candidates`, `error`, `generated`로 출력하며 후보별 실행 정보는 `command`, `setEnv`, `unsetEnv`에 둔다.

**`agent hooks` 동작**:
- 정책 파일은 기본적으로 `~/.config/quota/agent-hooks.d/*.json`에서 읽는다. 모든 `agent hooks` 하위 명령은 `--policy-dir <dir>`로 다른 정책 디렉터리를 지정할 수 있다.
- `init --preset=github-history-guard`는 PR/Issue 생성·수정 같은 GitHub 협업 메타데이터 작업은 허용하면서 `git push`, `git send-pack`, `git pull`, `git merge`, `git rebase`, `git commit --amend`, `git reset --hard`, `git filter-branch`, `git hook run`, `git for-each-repo`, `git update-ref`, `git replace`, `git reflog expire`, 강제 branch reset, branch delete/move/copy, tag force/delete, `git config alias.*`/`include.*`, shell `alias`/`source`/`.`/`trap`/`xargs`, `gh pr merge`, `gh pr update-branch`, `gh pr checkout/co --force`, `gh repo edit --visibility`, `gh repo sync`, `gh release create/delete`, raw `gh api`, `gh alias set/import/delete`, `gh extension exec`, 알 수 없는 `git`/`gh` alias·extension dispatch, 허용 형식 밖의 `gh stack ...`를 차단하는 기본 정책을 생성한다. 기존 정책 파일이 있으면 `--force` 없이는 덮어쓰지 않는다.
- `apply`는 enabled 정책이 최소 1개 없으면 hook 설정을 쓰지 않는다. 설정 파일이 이미 있으면 `<path>.bak.<timestamp>` 백업을 만든 뒤, 기존 managed evaluator hook만 교체하고 다른 hook은 보존한다. managed 여부는 evaluator 호출이 실제 실행 명령(command position)이고 apply가 설치하는 `--runtime=` 인자를 포함할 때만 인정하므로, 인자에 evaluator argv 문자열이 들어있을 뿐인 다른 hook은 교체하지 않는다. `--policy-dir`를 지정한 경우 설치되는 evaluator 명령도 같은 디렉터리를 인자로 받으며, `--binary`와 `--policy-dir`의 상대 경로는 적용 시점의 절대 경로로 고정한다.
- Claude 쪽 설치 대상은 `CLAUDE_CONFIG_DIR/settings.json` 또는 기본 `~/.claude/settings.json`의 `hooks.PreToolUse`/`matcher=Bash`다. Codex 쪽 설치 대상은 `CODEX_HOME/hooks.json` 또는 기본 `~/.codex/hooks.json`의 `hooks.PreToolUse`/`matcher=Bash`다.
- `eval`은 hook event의 `tool_input.command` 또는 `tool_input.cmd`를 읽고, `--command`가 있으면 그 문자열을 직접 평가한다. 허용이면 exit code 0, 차단이면 exit code 2와 차단 사유를 반환한다. 정책 파일 로드 오류나 enabled 정책 부재도 hook 경로에서는 차단 실패로 처리하지 않도록 exit code 2를 반환한다.
- shell command 평가는 `mvdan.cc/sh/v3/syntax` parser로 수행한다. `git`/`gh`의 대표 global option, `env`/`sudo`/`command`/`builtin`/`exec` wrapper, `env -S`와 결합 short option 형태, `eval`, `sh -c` 계열 nested script는 정규화해 본다. shell interpreter에 정적으로 볼 수 있는 `-c` script가 없거나 startup env/file 또는 interactive/login startup으로 숨은 script가 실행될 수 있으면 stdin/script 파일 내용을 증명할 수 없으므로 차단한다. 동적 명령어 이름(`$cmd ...`)이나 동적 wrapper script(`sh -c "$cmd"`), 보호 대상 명령의 동적 인자(`git "$subcommand" ...`)는 정적으로 안전성을 증명할 수 없으므로 차단한다. quote·escape 없이 glob metacharacter(`*`, `?`, `[`)를 포함하거나 실제 brace expansion을 일으키는 `{...}`(최상위에 `,` 또는 `..` sequence가 있는 그룹)을 포함한 단어도 셸이 확장하므로 정적 리터럴로 보지 않고 같은 규칙으로 처리한다. 반면 `HEAD@{u}`·`stash@{0}`처럼 확장을 일으키지 않는 `{...}`와 backslash로 escape된 metacharacter는 리터럴로 취급한다. `&&`/`;`/`|` 등으로 이어진 복합 명령은 모든 statement를 평가해 하나라도 차단이면 전체를 차단하고, 모든 statement가 허용일 때만 허용한다.
- `verify`는 정책 파일 스키마와 enabled 정책의 내장 테스트를 실행한다. glob 패턴은 `path.Match` 문법으로 로드/검증 시점에 확인하며, 잘못된 패턴은 정책/규칙을 명시한 오류로 load·verify를 실패시킨다. `doctor`는 enabled 정책 존재와 Claude/Codex managed hook 존재 여부를 확인한다. agent 런타임의 hook 신뢰·재로드 상태는 각 런타임이 담당하므로, 설정 파일에 hook이 있어도 새 세션 또는 hook 관리 화면에서 재로드가 필요할 수 있다.

`agent hooks` JSON 설치와 Claude overlay 설치는 대상 경로별 프로세스 간 잠금 안에서 최신 파일 읽기·수정·백업·고유 임시 파일 쓰기·rename·저장 결과 재읽기를 수행한다. 값이 동일하면 파일과 백업을 쓰지 않는다. 신규 파일은 0644에 프로세스 umask를 적용하고, 기존 파일의 권한은 보존한다. 기존 내용을 담는 임시 파일은 원본보다 넓은 접근 권한으로 생성하지 않는다. 기존 JSON 숫자와 다른 설정값을 보존하고 파싱/변환 오류면 원본을 덮어쓰지 않는다. 직렬화 보장은 이 저장 경로를 사용하는 quota 명령 사이에 한정되며 외부 편집기를 통제하지 않는다.

**`agent overlay` 동작**:
- spec 파일은 기본적으로 `~/.config/quota/agent-overlay.json`에서 읽고, 모든 서브커맨드가 `--spec <file>`로 재정의한다. spec 파일이 없으면 `init`을 제외한 모든 명령이 명시적 오류로 실패한다 — 값이 비었을 때 다른 계층에서 끌어오는 암시적 기본값/fallback은 없다.
- spec 스키마는 `version`(정수, 1만 지원. 다른 값은 명시 오류), 선택적 `claude`/`codex`/`verify` 섹션으로 구성된다. spec JSON은 알 수 없는 필드를 허용하지 않는다(중첩 구조 포함) — 오타 키는 조용히 무시하지 않고 명시 오류로 load를 실패시킨다. `claude.hooks`와 `codex.hooks`는 `이벤트이름 → [ { command } ]` 맵이며, hook 이벤트 이름은 고정 목록이 아니라 spec에 적힌 키를 그대로 쓴다. `claude.replaces`는 이전 spec 버전이 설치했던 command 문자열 배열로, apply가 교체 대상으로 삼는다 — 비교는 문자열 완전 일치(`==`)뿐이고 접두·패턴·argv[0] 해석은 없다. `codex` 엔트리는 `additionalContextLimit`를 선택적으로 가질 수 있고, `codex.settings`는 top-level 설정 키/값 맵이다. `verify`는 런타임별로 분리되어 `verify.claude.command`/`verify.codex.command`가 각각 argv 배열이다(전역 `verify.command`는 없다). 모든 hook `command`는 비어 있으면 검증 오류이고, 각 `verify.<runtime>.command`는 비어 있지 않은 argv[0]을 요구한다. 생략된 runtime 섹션은 `doctor`에서 `unconfigured`로 보고한다.
- `init`은 위 스키마를 그대로 담은 placeholder spec을 생성한다. 모든 command는 `/path/to/overlay-hook` 형태의 placeholder이며 사용자가 실제 명령으로 교체한다. 기존 파일은 `--force` 없이는 덮어쓰지 않고, 쓰기는 임시 파일 + rename으로 원자적이다.
- Claude 설치 객체와 Codex 안내 스니펫에 쓰는 기대 설정을 검사 기준으로 공유한다. `plan`, `doctor`, Claude 저장 후 검사는 같은 검사 결과를 사용한다. `plan`은 항목별 차이와 명시적 방해 조건을 표시한다. command 완전 일치는 소유권 기준이며 정상 설치의 충분조건이 아니다. 지원 형태와 일치하면 `present`, Claude의 변형/교체 대상은 `stale`, Codex의 변형은 `mismatch`, 없는 항목은 `missing`이다. 대상 파일 파싱 실패는 오류로 exit 1.
- `apply`의 Claude 대상은 `CLAUDE_CONFIG_DIR/settings.json` 또는 기본 `~/.claude/settings.json`의 `hooks.<event>`다. 기존 파일은 충돌 없는 `<path>.bak.<timestamp>` 백업(같은 초 반복 적용에도 기존 백업을 덮지 않음) 후 임시 파일 + rename으로 원자적으로 쓴다. managed 판정은 같은 이벤트에서 command 문자열이 (a) spec command와 완전 일치하거나 (b) `claude.replaces`에 든 command와 완전 일치하는 엔트리뿐이며, argv[0] 추론은 없다. 이 command 일치는 엔트리 형태와 무관하게 적용되므로(plain string, `type`이 다른 object 포함) managed command의 잘못된 변형도 남기지 않고 spec 버전으로 교체하며, 그 외 엔트리는 제거·수정하지 않는다. Codex apply는 지원하지 않는다 — `config.toml`은 주석·trust hash가 있는 대형 TOML이라 자동 재작성이 위험하므로 `codex apply is not supported in v1; use plan/doctor`로 명시 실패(exit 1)하고, `--runtime=all`에서는 Claude만 적용한 뒤 같은 안내를 남긴다.
- `doctor`의 최고 상태는 `installed`다. 이는 검사한 대상 파일에서 관리 항목이 지원 형태와 일치하고 검사 대상인 명시적 방해 조건이 없다는 뜻이며 실제 실행 성공을 보장하지 않는다. 누락/불일치/방해 조건은 원인과 함께 `degraded`, runtime 생략은 `unconfigured`, 파싱/변환 실패는 `error`다. degraded/error가 있으면 exit 1.
- 관리 hook은 기대 그룹 조건과 실행 객체를 함께 비교한다. 빈 `matcher`와 생략은 동등하며 문자열 `statusMessage`는 허용한다. 그 밖의 추가 필드(`if`, `args`, `async` 등)는 지원하지 않는 변형으로 보고한다. 이는 런타임에서 실행 불가능하다는 판정은 아니다. 중복 관리 명령도 정상으로 인정하지 않는다. Claude의 `disableAllHooks: true`, `allowManagedHooksOnly: true`, Codex의 `features.hooks = false`(정식 키가 없으면 이전 이름 `codex_hooks` 검사하며 두 키의 값이 다르면 지원하지 않는 조합으로 보고), `allow_managed_hooks_only = true`는 방해 조건이다. apply는 이 설정을 임의로 켜거나 해제하지 않고 저장 후 재검사에서 방해 조건을 보고한다.
- Codex는 `CODEX_HOME/config.toml` 또는 기본 `~/.codex/config.toml`을 읽기 전용으로 검사한다. 설정값과 `[[hooks.<event>]]` 그룹 아래 `[[hooks.<event>.hooks]]` 객체를 기대 설정과 비교한다. 누락 항목에는 추가 스니펫, 불일치 항목에는 교체 안내를 제공하며 다른 hook과 설정은 보존하도록 안내한다.
- JSON 숫자는 원문 정밀도를 유지해 읽는다. 정수값이고 signed int64 범위에 들면 표기와 무관하게 TOML 정수로 변환한다. 그 외 소수점이나 지수 표기가 있는 숫자는 유한 float64로 변환하며 범위 초과와 0으로의 언더플로를 거부한다. 소수점·지수 표기 없는 정수의 int64 범위 초과는 오류다. Claude의 `additionalContextLimit`, 음수 context limit, 동일 이벤트의 중복 command, `claude.replaces`와 현재 Claude hook command의 중복, `codex.settings.hooks`와 `codex.hooks`의 중복 정의는 입력 오류다. 임의 이벤트명·명령·설정 키를 받는 기능은 유지하며 전체 런타임 스키마를 검증하지 않는다.
- `verify`는 doctor와 동일한 엔트리 검사에 더해 런타임별 verify 명령을 실행한다. 엔트리가 설치된(installed) configured 런타임의 `verify.<runtime>.command`를 현재 작업 디렉터리에서 실행해 exit 0이면 그 런타임을 `enforced`로 올린다. verify 명령이 없으면 `degraded`("live verification not configured"), 명령이 non-zero면 `degraded`(원인에 exit code)다. `unconfigured` 런타임은 건너뛴다. 모든 configured 런타임이 `enforced`일 때만 exit 0이고, 하나라도 degraded/error면 exit 1.

`enforced`는 위 설정 검사에 더해 사용자가 지정한 실행 검증 명령이 통과했다는 뜻이다. 지침 전달 등 그 명령이 확인하지 않은 실행 조건까지 보장하지 않는다.

**서브커맨드 (세션 로그 조회)**:
| 명령 | 설명 |
|------|------|
| `quota-cli session-log list [-agent all\|claude\|codex] [-account key] [-limit N] [-json]` | configured 계정들의 세션 로그 파일을 최근 수정 순으로 출력 |
| `quota-cli session-log search [-agent all\|claude\|codex] [-account key] [-limit N] [-max-chars N] [-include-tools] [-json] <query>` | user/assistant 텍스트에서 query를 찾아 제한된 snippet만 출력 |
| `quota-cli session-log show [-agent all\|claude\|codex] [-account key] [-tail N] [-max-chars N] [-include-tools] [-json] <session-ref>` | session-ref가 가리키는 로그에서 최근 메시지만 출력 |

- 세션 로그 조회는 읽기 전용이다. 로그 파일을 삭제, 이동, 수정, compact하지 않는다.
- 기본 범위는 `agent=all`이며 `account`를 지정하면 해당 key만 본다. `claude-2`, `codex-2` 같은 추가 계정은 기존 `config.json` 계정 설정에서 로그 root를 계산한다.
- Claude 기본 계정 로그 root는 `CLAUDE_PROJECTS_DIR`, `CLAUDE_CONFIG_DIR/projects`, `~/.claude/projects` 순서로 정한다. 추가 Claude 계정은 `<configDir>/projects`를 본다.
- Codex 기본 계정 로그 root는 `CODEX_SESSIONS_DIR`, `CODEX_HOME/sessions`, `~/.codex/sessions` 순서로 정한다. 추가 Codex 계정은 `<home>/sessions`를 본다.
- 기본 출력은 user/assistant 메시지 텍스트만 포함한다. tool call/result 원문은 `--include-tools`가 있을 때만 검색/출력한다.
- 토큰 소모를 제한하기 위해 `search` 기본값은 `limit=20`, `max-chars=220`이고, `show` 기본값은 `tail=40`, `max-chars=880`이다. `max-chars=0`은 해당 truncation을 끈다.
- `show`의 `session-ref`는 configured 로그 root 아래 파일의 정확한 path, basename, 또는 path 부분 문자열로 해석한다. 여러 파일이 맞으면 후보를 출력하고 실패한다.

- 첫 인자가 `account`/`update`/`exec-prompt`/`select-agent`/`session-log`이면 해당 서브커맨드로 동작한다. 그 외 조회 모드는 `quota-cli [-json] [-timeout N]` 형태만 허용하며, 알 수 없는 positional 인자가 남으면 실행하지 않고 usage와 함께 실패한다.
- 검증 규칙은 조회 시 `config.json`을 읽는 규칙과 동일하다(같은 형식/중복 규칙). Claude는 `^claude-\d+$`, Codex는 `^codex-\d+$`.

**동작**:
1. `~/.config/quota/config.json`에서 추가 Claude/Codex 계정 목록을 읽는다 (파일 없거나 목록 비면 각 기본 계정만).
2. 기본 Claude 계정 + 추가 Claude 계정을 각각 `claude.GetQuotaForConfigDir`로 조회 (maxAge 60초 — 캐시가 60초 이내면 재사용, 아니면 실측 후 캐시 갱신)
3. 기본 Codex 계정 + 추가 Codex 계정을 각각 `codex.GetQuotaForHome`로 조회 (기본 계정은 `home=""`, maxAge 60초)
4. 2·3은 모두 **병렬** 조회. 모두 완료되면 결과를 JSON 또는 텍스트로 출력
5. 개별 provider/계정 에러는 errors 배열에 포함, 프로세스 자체는 종료하지 않음

**JSON 출력 형식**:
```json
{
  "claude":   { ... },
  "claude-2": { ... },
  "codex":    { ... },
  "codex-2":  { ... },
  "errors": []
}
```
- 기본 Claude/Codex 계정은 항상 `claude`/`codex` 키.
- 추가 계정은 `config.json`에 명시한 `key`(예: `claude-2`, `codex-2`)로 top-level에 배치한다. 각 값은 해당 provider의 quota 구조와 동일하다.
- 계정 조회 실패는 해당 top-level 키를 생략하고 `errors`에 계정 key와 함께 기록한다 (부분 결과 허용).

**설정 파일**: `~/.config/quota/config.json`
```json
{
  "claudeAccounts": [
    { "key": "claude-2", "configDir": "~/.claude-2" }
  ],
  "codexAccounts": [
    { "key": "codex-2", "home": "~/.codex-alt" }
  ],
  "execPrompt": {
    "accountSettings": {
      "claude": { "minLeftPct": 40 },
      "claude-2": { "minLeftPct": 5 },
      "codex": { "minLeftPct": 30 },
      "codex-2": { "minLeftPct": 5 }
    }
  }
}
```
- `claudeAccounts` (optional): 기본 계정 외에 추가로 조회할 Claude 계정 목록.
  - `key` (string, 필수): 출력 top-level 키. **`claude-<정수>` 형식이어야 한다**(정규식 `^claude-\d+$`, 예: `claude-2`, `claude-3`). 기본 계정 `claude` 및 다른 항목과 중복 불가. (소비자는 `^claude-?\d+$`로 추가 provider를 인식한다.)
  - `configDir` (string, 필수): 해당 계정의 Claude config 디렉터리. `~`는 홈으로 확장된다. 유저가 직접 지정하며, 외부 도구의 설정을 참조하지 않는다. 서로 다른 계정은 서로 다른 `configDir`를 가리켜야 한다.
- `codexAccounts` (optional): 기본 계정 외에 추가로 조회할 Codex 계정 목록. `claudeAccounts`와 **대칭 구조**다.
  - `key` (string, 필수): 출력 top-level 키. **`codex-<정수>` 형식이어야 한다**(정규식 `^codex-\d+$`, 예: `codex-2`). 기본 계정 `codex` 및 다른 항목과 중복 불가.
  - `home` (string, 필수): 해당 계정의 `CODEX_HOME` 디렉터리. `~`는 홈으로 확장된다. 서로 다른 계정은 서로 다른 `home`을 가리켜야 한다. 각 home에는 **동일/다른 계정을 별도 로그인**해 두어야 한다(인증 파일 복사가 아니라 `CODEX_HOME=<home> codex login`).
- `execPrompt.accountSettings` (optional): 프롬프트 실행/agent 선택용 계정별 설정. `exec-prompt`와 `select-agent`가 사용한다. 키는 `claude`, `claude-<정수>`, `codex`, `codex-<정수>`만 허용한다. 현재 필드는 `minLeftPct`뿐이며 없으면 5, 값은 0 이상 100 이하의 숫자여야 한다. 이 설정은 조회 출력과 quota-bar 표시에 영향을 주지 않는다.
- 파일이 없거나 목록이 비면 각 기본 계정만 조회한다(기존 동작).
- 다음 항목은 건너뛰고 `errors`에 기록한다: 빈 `key`/`dir`, 형식 위반, 중복 `key`, 중복 `dir`. (중복 `configDir`/`home`은 같은 계정을 두 번 조회하는 설정 오류이므로 금지.)

### quota-bar

**용도**: macOS 메뉴바에 상주하며 quota를 주기적으로 갱신

실행 시 자동으로 백그라운드 프로세스로 전환된다 (`&` 불필요).
중복 실행 방지: `~/.config/quota/quota-bar.pid` 파일에 flock을 획득하여 단일 인스턴스만 실행된다. 이미 실행 중이면 즉시 종료.

**동작**:
1. 시작 시 `~/.config/quota/quota-bar.json`에서 화면 설정(선택 항목) 로드
2. `~/.config/quota/config.json`을 `config.ResolveAccounts()`(Claude) + `config.ResolveCodexAccounts()`(Codex)로 해석해 조회할 계정 목록 확정 (각 기본 `claude`/`codex` + 유효한 추가 계정). quota-cli와 동일한 규칙·순서를 공유한다. skip된 항목은 로그로만 기록.
3. systray 아이콘 + 메뉴 구성 (Claude 계정별 그룹 + Codex 계정별 그룹)
4. 즉시 1회 refresh 실행, 이후 활동 기반 간격으로 자동 refresh (활성/idle 주기는 `quota-bar.json`으로 설정 가능, 기본 3분/30분)
5. **refresh = 각 Claude 계정 `claude.GetQuotaForConfigDir(timeout, configDir, maxAge)` (기본 `configDir=""`) + 각 Codex 계정 `codex.GetQuotaForHome(timeout, home, maxAge)` (기본 `home=""`)를 병렬 호출** (내부 패키지). maxAge는 활성/idle 중 더 짧은 refresh 주기의 절반이며 상한은 180초 — 공유 캐시(§공유 캐시)가 그보다 최근이면 재사용하고, 아니면 실측 후 캐시에 기록한다.
6. 결과를 메뉴 항목에 표시

**계정 목록 확정 시점 (중요)**: systray는 런타임에 메뉴 항목을 추가·제거할 수 없다. 따라서 계정 수와 메뉴 레이아웃은 **onReady 시작 시점의 config로 고정**된다. `config.json`을 편집해 계정을 추가/제거하면 **quota-bar를 재시작**해야 반영된다.

**수동 업데이트 메뉴 ("Check for Updates…")**: 클릭 시 quota-cli의 `update`와 같은 비교·설치 흐름(`internal/update`)을 수행하고, 설치 성공 시 새 바이너리로 프로세스를 넘긴다. launchd job이 설치 경로를 가리키면 종료해서 launchd가 재시작하게 하고, 그 외에는 새 프로세스를 spawn한 뒤 현재 프로세스가 종료한다. in-place `syscall.Exec`은 사용하지 않는다.
- **refresh 게이트는 handover 직전에만 획득한다**(최대 3분 대기) — 조회 중 handover하면 진행 중인 `claude`/`codex` 프로브 프로세스가 종료 관리 범위 밖에 남을 수 있기 때문이고, 확인·설치는 게이트가 필요 없다(설치는 파일 쓰기일 뿐). 성공 경로에서는 게이트를 반환하지 않는다(프로세스가 종료되므로).
- **게이트를 쥔 동안 systray 호출을 하지 않는다.** systray의 모든 메뉴 조작은 Cocoa 메인 스레드 동기 디스패치(`waitUntilDone:YES`)라서 메뉴 닫힘과 경합하면 재시작 전까지 블로킹될 수 있다. 그래서 (1) 클릭 직후 첫 조작 전에 짧게 대기하고, (2) 모든 상태 전이는 **로그를 먼저 남긴 뒤** 화면에 반영하며, (3) 워치독이 10분 내 미완료 흐름을 로그로 알린다. systray 조작이 블로킹되어도 게이트가 없으므로 refresh 루프는 계속 돈다.
- **버튼과 상태는 분리된 표면이다**: 버튼 제목은 항상 "Check for Updates…"로 불변이며 클릭의 의미는 언제나 "지금 확인" 하나다. 진행 상태와 결과(최신임/실패)는 버튼 아래 별도 비활성 상태 행에 표시하고, 마지막 결과는 다음 확인 때까지 유지된다(첫 사용 전에는 상태 행 숨김). 흐름이 도는 동안 버튼은 비활성화된다 — 클릭이 조용히 무시되는 상태를 만들지 않는다. 실패는 로그에도 남긴다.
- 수동 전용: 자동 체크·자동 설치는 없다.
- 알려진 제약: go-install 경로가 아닌 바이너리(예: dev 체크아웃 빌드)를 Start at Login으로 등록한 경우, 업데이트 직후에는 go-install 경로의 새 바이너리로 넘기지만 launchd plist는 등록 시점의 옛 경로를 그대로 가리키므로 다음 launchd 재시작 때 옛 바이너리로 돌아갈 수 있다. `go install`로 설치한 표준 경로에서는 경로가 일치해 문제없다.

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
☐ Week 90% (2d 5h)
☐ Fable 80%
── Claude 2 ──
☐ Session 40% (3h 10m)
☐ Week 55% (1d 8h)
── Codex ──
☑ 5h 85% (2h 30m)
☐ 7d 70% (6d 19h)
   Reset credits: 2  (1d 0h)  ▸
                              ├ 1d 0h
                              └ 6d 23h
── Codex 2 ──
☐ 7d 45% (5d 8h)
───────────────
Updated 14:30
Reset as clock time
Refresh
Start at Login
quota-bar v0.4.0
Quit
```

- 각 항목: `라벨 XX% (남은시간)` 형식. 남은시간 없으면 괄호 생략.
- `Reset as clock time` 체크 시 각 항목의 괄호를 남은시간 대신 절대 시각으로 표시한다: `Weekly 90% (Mon Jul 6 15:04)`. 절대 시각을 모르는 항목(`resetsAt` 없음)은 남은시간으로 유지한다.

**Codex 초기화권 (Reset credits 행)**:
- 라벨은 `Reset credits`다 — Codex 공식 표현(응답 필드 `rateLimitResetCredits`)을 따르며 "초기화권"(초기화=reset, 권=credit) 의미를 담는다.
- Codex `resetCredits`(초기화권)가 있으면 **해당 Codex 계정 섹션마다** `Reset credits: N` 부모 행을 두고, 각 초기화권 만료를 **서브메뉴**로 나열한다(만료 임박순). `N`은 사용 가능 수(`= 나열한 자식 수`), 부모의 괄호는 가장 임박한 만료다(수식어 없이 시각만). 계정별로 부모·슬롯을 각각 미리 만든다.
- **표시 전용**이다: 체크박스 아님, 상단 % 바에 넣지 않는다. 부모·자식 모두 enable 상태로 두어(가독성 위해 — disable 회색 텍스트를 피한다) 클릭은 아무 동작 없이 무시된다(정보 행). 부모 행에는 툴팁을 달지 않는다(뷰를 가림).
- 부모/자식 시각도 **`Reset as clock time` 토글을 그대로 따른다** — off면 남은시간(`1d 0h`), on이면 절대 시각(`Sun Jul 12 10:42`). 절대 시각을 모르면 남은시간으로 유지하고, 남은시간·절대 시각 모두 없으면 grant 제목으로 대체한다.
- 자식 슬롯은 고정 개수(`resetCreditSlots`)를 미리 만들어 두고 데이터 수만큼 show/hide 한다(systray 런타임 항목 추가 불가). 사용 가능 초기화권이 슬롯 수를 초과하면 부모 카운트는 실제 수를 표시하고 서브메뉴는 임박한 슬롯 수만 보여준다. 사용 가능 초기화권이 없으면 부모·자식 모두 숨긴다.
- 에러 발생 시 해당 계정/영역에 에러 메시지 표시 (계정별·Codex별 에러 행을 각각 둔다).
- 마지막 갱신 시간은 하단에 표시

**다중 Claude 계정 표시**:
- 추가 계정이 등록돼 있으면 기본 `Claude` 그룹 아래에 계정별 그룹을 순서대로 만든다. 그룹 헤더 라벨은 계정 key에서 파생한다: `claude`→`Claude`, `claude-2`→`Claude 2` (`ResolvedAccount.Label`).
- 각 그룹은 자기 **윈도우 슬롯**(아래 "동적 창 슬롯" 참조)을 가진다.
- Codex 그룹들은 항상 모든 Claude 그룹 뒤에 온다.

**다중 Codex 계정 표시**:
- Claude와 **대칭**이다. 추가 Codex 계정이 등록돼 있으면 기본 `Codex` 그룹 뒤에 계정별 그룹을 순서대로 만든다. 그룹 헤더 라벨은 `codex`→`Codex`, `codex-2`→`Codex 2` (`ResolvedCodexAccount.Label`).
- 각 그룹은 자기 **윈도우 슬롯**(아래 "동적 창 슬롯", Claude와 동일 메커니즘) + 자기 **Reset credits(초기화권)** 부모 행·서브메뉴(아래 참조)를 가진다. 초기화권은 계정별로 독립이며 서로 섞이지 않는다.
- 같은 과금 계정을 다른 `CODEX_HOME`에 로그인한 경우, quota 숫자·초기화권은 서버측 계정 단위라 두 그룹이 동일하게 나온다(로컬 격리만 다름).

**메뉴 항목 키 스킴** (설정 파일 하위호환):
- 키 형식은 `<provider>_<window key>`이며 `provider`는 계정 key(`claude`, `claude-2`, …, `codex`, `codex-2`, …), `window key`는 그 provider `WindowKeys()`의 값이다. 계정 key는 `^claude-\d+$`/`^codex-\d+$`(또는 기본 `claude`/`codex`)라 `_`를 포함하지 않으므로, 첫 `_`가 항상 provider와 window key를 가른다.
- **모든 창 키는 슬롯/선택 식별자일 뿐 화면 라벨이 아니다** — 화면 라벨은 언제나 데이터의 `label`이다.
- Claude: `claude_session`, `claude_weekly_all`, `claude_extra_1`~`claude_extra_3` (**기존 그대로 유지**).
- Codex: `codex_5h`, `codex_daily`, `codex_weekly`, `codex_monthly`. 구 `codex_day`(주간 윈도우를 담던 슬롯)는 로드 시 `codex_weekly`로 마이그레이션되어 기존 선택이 유효하게 유지된다.
- 추가 계정은 동일 스킴을 계정 key로 확장한다: `claude-2_session`~, `codex-2_5h`~.
- 선택 상태(체크)는 이 슬롯 키로 저장되므로 모델명 변경·창 재배치·기간 변경이 있어도 유지된다.

**동적 창 슬롯** (provider 무관, 단일 메커니즘):
- 계정 그룹마다 그 provider의 `WindowKeys()` 어휘 하나당 체크박스 슬롯을 **미리 숨김 상태로** 만든다(systray는 런타임에 행을 추가·제거할 수 없다 — 소비자가 유한 키 어휘를 필요로 하는 유일한 이유).
- refresh 시 그 계정의 `windows` 목록을 순회해 각 항목을 자기 `key` 슬롯(`<account>_<key>`)에 채우고, **행 라벨은 그 항목의 `label`을 그대로** 쓴다. quota-bar는 어떤 창 이름도 갖지 않는다.
- **모든 창 행은 동적이다**: 이번 refresh가 그 창을 줬을 때만(=라벨이 있을 때만) 보이고, 없으면 숨긴다. 어떤 provider도 고정된 창 집합을 보장하지 않기 때문이다.
- 그래서 창이 사라지거나(Codex 5h 소멸) 되살아나거나(5h 재등장) 새 tier가 생겨도(주간→월간 교체) **코드 변경 없이** 각 창이 자기 라벨로 표시된다.
- 유한 슬롯의 대가는 **이 소비자 안에서만** 치른다: 같은 슬롯에 두 창이 오면 첫 창만 보이고, `WindowKeys()` 밖의 key(예: `extra_4`)는 슬롯이 없어 무시된다. **데이터(`quota-cli`)는 그 창들을 전부 갖고 있다.**

**설정 파일**: `~/.config/quota/quota-bar.json`
```json
{ "selected": ["codex_5h", "claude_session"], "showResetTime": false, "refreshActiveMinutes": 30 }
```

- `selected` (string 배열): 상단 바에 표시할 체크된 항목 키.
- `showResetTime` (bool, optional, 기본 false): true면 각 항목의 리셋을 남은시간 대신 절대 시각(`FormatResetAt`, 예: `Mon Jul 6 15:04`)으로 표시. Codex 초기화권(Reset credits 부모·서브메뉴)의 만료 시각도 동일하게 따른다. 메뉴의 `Reset as clock time` 체크로 토글하며, 즉시 저장 후 재조회 없이 메뉴만 다시 그린다(초기화권 행 포함). 상단 바(퍼센트 전용)에는 영향 없다.
- `refreshActiveMinutes` (int, optional): 활성 상태 refresh 주기(분). **없거나 0 이하면 앱 기본값 3분**을 쓴다(명시 기본값 — 암시 fallback 아님). 값이 있으면 그 값을 적용한다.
- `refreshIdleMinutes` (int, optional): idle 상태 refresh 주기(분). **없거나 0 이하면 앱 기본값 30분**. stale 경고 임계는 두 주기(active/idle) 중 **큰 값 + 5분**으로 계산해, 어느 주기를 늘려도(활성 주기를 idle보다 크게 잡아도) 정상 갱신을 stale로 오판하지 않는다.
  - 두 주기는 **메뉴에 토글이 없다**. 파일로만 지정하며, 변경은 계정 목록과 마찬가지로 **quota-bar 재시작 후 반영**된다(시작 시 고정).
  - Codex 초기화권은 rate limit 응답에 함께 오므로 별도 주기가 없다. 이 주기는 codex/claude 조회 전체에 적용된다.
- 마이그레이션: 로드 시 구 키를 1회 치환 후 저장한다 (중복 제거 포함) — `claude_weekly_sonnet`→`claude_extra_1`, 그리고 구 Codex `<account>_day`(주간 윈도우를 담던 슬롯)→`<account>_weekly`.

**에러 처리**:
- refresh 실패 시 이전 성공 데이터(lastOK)를 유지하고 에러 메시지만 표시
- 동시 refresh 방지 (mutex + running flag)

**Stale 데이터 경고**:
- provider별 마지막 성공 시각(`lastSuccessAt`)을 추적한다. provider는 각 Claude 계정 key(`claude`, `claude-2`, …)와 각 Codex 계정 key(`codex`, `codex-2`, …)다.
- 해당 provider가 stale 임계 시간 이상 갱신 실패 시, 그 provider의 데이터에 `?` 접미사를 붙여 표시
  - 바 타이틀: `95%` → `95%?`
  - 메뉴 항목: `Session 95%` → `Session 95%?`
- "Updated" 행에 경과 시간 표시: `Updated 14:30 (claude 5m0s ago!)`
- carry/snapshot(실패 계정의 직전 성공값 유지, 성공 계정의 값 갱신)은 정확히 `<provider>_` prefix로 계정별로 분리된다. 계정 key에 `_`가 없어 `claude_`가 `claude-2_` 행을 잘못 매칭하지 않는다.

**로그**:
- 데몬 프로세스 로그 출력: `~/.config/quota/quota-bar.log`
- 에러, 시작/종료 이벤트 등을 기록

---

## 공유 캐시

파일: `~/.config/quota/quota-cache.json` (구현: `internal/quotacache`)

quota-cli·quota-bar·위임 실행이 공유하는, 계정별 **마지막 성공 조회의 파싱 전 raw 출력** 저장소. 각 소비자는 자기 신선도 기준에 맞는 값이 있으면 재사용한다.

- **키 = 실제 조회된 경로**(계정 이름이 아님): Claude는 해석된 `CLAUDE_CONFIG_DIR`(기본 계정은 상속값 또는 `~/.claude`), Codex는 해석된 `CODEX_HOME`(기본 계정은 상속값 또는 `~/.codex`). 상속 환경이 다르면 다른 키가 되어, 한 환경의 기본 계정 값이 다른 환경 실행에 잘못 제공되지 않는다.
- **파싱 전 raw만 저장한다.** 파싱된 결과는 `time.Time`/`int`/`[]map[string]any` 등 Go 타입을 담고 있어 JSON 왕복으로 깨진다(→ string/float64/[]any). 읽을 때 재파싱해 타입 손상을 피한다. 상대 리셋만 있는 provider 출력은 절대 변경 시각을 알 수 없으므로 신선도 기준으로만 제한된다.
- **성공만 저장한다.** 조회 실패는 캐시하지 않아 일시적 실패가 굳지 않고 매번 재시도된다.
- **데이터 변경 경계에서 무효화한다.** 저장 시 가장 이른 창 리셋 또는 사용 가능한 초기화권 만료 시각을 함께 기록하고, 그 시각이 지나면 신선도 기준 이내라도 히트를 거부한다. 상대 리셋만 있는 창은 절대 시각이 없어 신선도 기준만 적용된다.
- **읽기**: 소비자가 자기 신선도 기준(cli/위임 60초, bar는 활성/idle 중 더 짧은 refresh 주기의 절반·상한 180초) 이내이고 데이터 변경 경계 전이면 재사용하고, miss이면 그 계정을 실측해 기록한다. 동시 miss는 각각 실측할 수 있으며 이후 조회부터 캐시를 공유한다.
- **쓰기**: sidecar 파일 flock으로 직렬화한 read-modify-write + temp→rename 원자적 교체(파일 권한 0o600 — raw에 계정 사용 패턴이 담긴다). 일부 계정만 조회한 소비자가 다른 계정 항목을 덮어쓰지 않는다.

## Internal 패키지 사양

### internal/claude

**함수**:
- `GetQuota(timeout time.Duration) (map[string]any, error)` — 기본 계정 조회 (config-dir 미지정, 항상 실측 = maxAge 0)
- `GetQuotaForConfigDir(timeout time.Duration, configDir string, maxAge time.Duration) (map[string]any, error)` — 지정한 `CLAUDE_CONFIG_DIR` 계정 조회. `configDir`가 빈 문자열이면 기본 계정. 공유 캐시(§공유 캐시)에 이 계정의 마지막 조회가 `maxAge` 이내로 있으면 그 raw를 재파싱해 반환하고, 실측 시 결과를 캐시에 기록한다. `maxAge`가 0 이하면 캐시 읽기를 건너뛰되 성공 시 갱신은 한다.
- `WindowKeys() []string` — 슬롯을 미리 만들어야 하는 소비자(quota-bar)가 열거하는 **소비자 힌트**(표시 순서). 데이터의 상한이 아니다 — 모델별 행이 더 많으면 `parseUsage`는 `extra_4` 이상도 반환하고, 슬롯이 없는 소비자만 그것을 무시한다.

- Claude CLI를 **headless(`-p`)로 1회 실행**해 quota 조회. 두 함수는 동일한 조회 로직을 공유하며 config-dir 주입 여부만 다르다.

**조회 흐름**:
1. `claude -p "/usage" --output-format json` 을 실행한다 (PATH의 `claude`, 없으면 `~/.local/bin/claude`).
   - `/usage`는 **로컬 슬래시 커맨드**라 턴을 소비하지 않는다(`num_turns` 0, `total_cost_usd` 0). quota를 재려고 quota를 쓰지 않으므로 refresh 주기로 반복 호출해도 된다.
   - 작업 디렉터리 = `~/.config/quota`. Claude CLI는 CWD를 프로젝트 루트로 보고 readdir하므로, 홈에서 실행하면 macOS TCC 보호 폴더(Downloads, Photos, Music, Movies)를 건드린다.
2. 프로세스 환경 (`fetchEnv`):
   - `CLAUDECODE` 제거: 중첩 세션 감지 회피
   - `ANTHROPIC_API_HOST` / `ANTHROPIC_API_KEY` / `ANTHROPIC_AUTH_TOKEN` / `ANTHROPIC_BASE_URL` / `CLAUDE_API_KEY` / `CLAUDE_CODE_API_BASE_URL` / `CLAUDE_CODE_OAUTH_REFRESH_TOKEN` / `CLAUDE_CODE_OAUTH_TOKEN` 제거: 사용자 로그인 계정 quota를 읽도록 강제 (커스텀 엔드포인트/대체 토큰이 quota를 가로채지 않게)
   - `CLAUDE_CONFIG_DIR`: config-dir 지정 시 **상속값을 제거하고 지정값을 넣는다**(같은 이름의 할당을 두 번 두지 않는다 — 어느 쪽이 이길지는 OS가 정하므로, 지면 다른 계정 키 아래에 자기 계정 값이 실린다). config-dir 미지정 시에는 상속값을 그대로 둔다 — 그것이 호출자의 기본 계정을 고르는 방식이다.
3. timeout 초과 시 context로 프로세스를 종료하고 timeout 에러를 반환한다. 실행 실패는 stderr(없으면 stdout) 앞부분을 붙여 에러로 반환한다.
4. stdout의 JSON 엔벨로프를 파싱(`usageText`)해 `result`(사람이 읽는 /usage 리포트)를 꺼낸다. `is_error: true`면 CLI가 준 메시지를 담아 에러로 실패한다 — 에러 엔벨로프에는 사용량 행이 없으므로 그대로 파싱하면 원인 대신 파싱 실패로 보인다.
5. `parseUsage()` 로 파싱.

**파싱 로직** (`parseUsage`) — 줄 단위 파싱, 공통 `windows` 목록(리포트 순서)으로 반환:
- 리포트의 각 사용량 행은 **한 줄**에 라벨·퍼센트·리셋 시각을 담는다:
  `Current week (all models): 35% used · resets Aug 3 at 12pm (Asia/Seoul)`
  창이 아직 시작되지 않았으면 리셋 절이 없다: `Current session: 0% used`
- 행 매칭(`usageRowRe`) = `^(Current\s+.*?):\s*(\d+)%\s+used\b(.*)$` — 라벨은 `Current `로 시작해 첫 콜론까지다. `Current ` 접두를 요구하는 이유는 그것이 quota 행과 나머지 리포트를 가르는 경계이기 때문이다 — 하단 섹션은 퍼센트투성이라 느슨한 패턴이면 `Last 24h: 73% used by …` 같은 한 줄이 모델별 행으로 섞여 들어간다. 트레이드오프는 의도적이다: 접두가 사라지면 행을 잃고 조회가 시끄럽게 실패하지만(복구 가능), false match는 quota가 아닌 숫자를 조용히 보고한다. 섹션 헤더 문구 대신 접두에 앵커하므로 버전마다 바뀌는 장식 문구와 무관하다.
- 리셋 절은 행 꼬리에서 별도로 뽑는다(`resetsClauseRe`). 퍼센트와 리셋 절 사이 구분자는 고정하지 않는다 — Claude 버전마다 장식 문구·글리프가 바뀌므로, 구분자가 바뀌었다고 리셋 시각을 잃으면 안 된다.
- **key(구조적 슬롯 식별자) 분류**:
  - `Current session` 포함 → `session`
  - `all models` 포함 → `weekly_all`
  - 그 외(모델별 행) → `extra_N` (리포트 순서, 라벨 중복 제거, 개수 제한 없음)
- **label**: 콜론 앞 텍스트에서 `windowLabel`로 도출한다(하드코딩 어휘 없음 — 데이터 모델의 Claude 절 참조)
- resets: 상대시간(`resetsIn`)으로 정규화하고, 절대표기를 파싱할 수 있으면 절대 리셋 시각(`resetsAt`, `time.Time`)도 함께 채운다 (`parseReset`). 리셋 절이 없는 행은 두 키 모두 생략한다.
- 일부 행이 리포트에 없거나 매칭이 일부만 되어도 매치된 항목만 반환 (부분 결과 허용)
- 하단 "What's contributing" 섹션은 퍼센트투성이지만 매치되지 않는다: 그 줄들은 콜론이 없거나(`73% of your usage came from …`) 콜론 뒤가 `N% used`가 아니다(`Top skills: /skill-one 1%`).
- ANSI 이스케이프는 파싱 전에 제거한다. JSON 엔벨로프는 지금까지 깨끗한 텍스트만 실어왔지만, 리포트에 장식이 붙는 날 전 행이 한꺼번에 매치 실패하는 것을 막는다.

### internal/codex

**함수**:
- `GetQuota(timeout time.Duration) (map[string]any, error)` — 기본 계정 조회 (CODEX_HOME 미주입, 항상 실측 = maxAge 0)
- `GetQuotaForHome(timeout time.Duration, codexHome string, maxAge time.Duration) (map[string]any, error)` — 지정한 `CODEX_HOME` 계정 조회. `codexHome`가 빈 문자열이면 기본 계정. `codexHome`는 이미 확장된 절대경로여야 하며, 호출자가 `config.ExpandTilde`로 확장해 넘긴다(Claude `GetQuotaForConfigDir`와 대칭). 공유 캐시 동작은 `GetQuotaForConfigDir`와 동일하다.

**동작**:
1. `codex app-server` 프로세스를 시작 (stdin/stdout pipe). `codexHome`가 비어있지 않으면 상속된 `CODEX_HOME`을 제거하고 `CODEX_HOME=<codexHome>`을 하나만 주입한다. 빈 문자열이면 상속된 `CODEX_HOME`을 유지한다. 두 경우 모두 로그인한 home 계정 대신 환경 override가 사용되지 않도록 `CODEX_ACCESS_TOKEN`/`CODEX_API_KEY`/`CODEX_AUTH`/`CODEX_AUTHAPI_BASE_URL`/`CODEX_URL`과 `OPENAI_API_KEY`/`OPENAI_BASE_URL`/`OPENAI_ORGANIZATION`/`OPENAI_PROJECT`를 제거한다.
2. JSON-RPC 2.0 프로토콜:
   - Request #1: `initialize` (clientInfo 전달)
   - Request #2: `account/rateLimits/read`
3. Response에서 `rateLimits` 또는 `rateLimitsByLimitId.codex` 추출
4. primary/secondary 각 윈도우를 **위치 무관**하게, `windowMins`(원본 duration)와 그것에서 진실하게 만든 `label`(`windowLabel`: 300→`5h`, 600→`10h`, 10080→`7d`)을 내장한 항목으로 `windows` 목록에 담는다(실제 기간 오름차순 정렬, 동일 `windowMins` 중복은 첫 항목만). `windowDurationMins`가 없는 윈도우는 생략. **범위 버킷으로 라벨을 뭉개지 않는다.**
5. `resetsAt` (epoch) → 상대 시간 문자열(`resetsIn`: `2h 30m`, `1d 3h` 등) + 절대 시각(`resetsAt`: `time.Time`) 함께 반환
6. top-level `rateLimitResetCredits`(초기화권) → status `available`인 grant만 만료 임박순으로 `resetCredits`(`available` + `items`)로 반환. 사용 가능한 grant가 없으면 키 생략.
7. 프로세스 종료

### internal/render

**함수**:
- `Text(payload map[string]any) string`
- `FormatResetAt(t time.Time) string` — 절대 리셋 시각을 `Mon Jan 2 15:04`(로컬 tz, 24시간제, **요일 선두**, **날짜 항상 포함**, 연도 생략)로 포맷. quota-bar와 공유하는 단일 포맷 소스. 쿼터는 주 단위로 리셋되므로 사용자가 실제로 알아야 하는 값은 요일이다 — 날짜만 주고 요일을 역산하게 만들지 않는다. 날짜를 함께 두어 일주일 이상 뒤도 구분되게 하고, 표시 대상 중 가장 긴 것이 30일대 초기화권이라 `요일+월+일`이면 연도 없이도 모호하지 않다.

- Claude, Codex 데이터를 사람이 읽을 수 있는 텍스트로 변환
- 계정별 섹션: Claude는 `claude`+`claude-N`, Codex는 `codex`+`codex-N`을 각각 기본 먼저·숫자 오름차순으로 헤더(`Claude`/`Claude 2`, `Codex`/`Codex 2`)와 함께 렌더한다. Claude 그룹들 뒤에 Codex 그룹들이 온다.
- **provider 분기 없음**: 각 그룹은 자기 `windows` 목록을 순회해 각 항목을 **그 항목의 `label`로** 렌더한다(목록이 이미 정식 순서). render는 어떤 창 이름도 갖지 않으므로, 새 창 종류·기간 변경·새 provider도 그대로 표시된다.
- 리셋 시간이 있으면 `(남은시간, at 절대시각)` 형식으로 표시 (예: `(2d 5h, at Mon Jul 6 15:04)`)
- Codex `resetCredits`(초기화권)가 있으면 Codex 섹션에 `Reset credits: N  (expires 절대시각)` 요약 줄을 추가한다. `N`은 사용 가능 수, 절대시각은 가장 임박한 만료(`items[0]`). 전체 목록은 `--json`으로 확인한다.
- 절대시각은 항목에 `resetsAt`가 있으면 그 값을 `FormatResetAt`로 포맷한다(역산 없이 정확).
- `resetsAt`가 없으면 `resetsIn`을 현재시각 기준으로 역산한다(`endTime`, 하위호환 fallback). 역산 결과도 `FormatResetAt`로 포맷한다 — 남은 시간이 얼마든 한 가지 모양만 나오므로, 같은 출력 안에서 행마다 형태가 달라지지 않는다.
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
| `mvdan.cc/sh/v3` | agent hook command parser |

시스템 의존성:
- `claude` CLI: Claude Code CLI (PATH 또는 `~/.local/bin/claude`). `-p`로 `/usage`를 실행할 수 있어야 한다 — 2.1.214~2.1.235에서 확인.
- `codex` CLI: Codex CLI (PATH에 있어야 함)
- `go`: 수동 업데이트(`quota-cli update`, quota-bar 업데이트 메뉴)에만 필요

---

## 현재 알려진 이슈

없음.
