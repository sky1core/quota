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

**용도**: 터미널에서 quota를 한번 조회하고 결과 출력

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
| `quota-cli account rm <key>` | 계정 제거. `codex-<N>`이면 Codex 목록에서, 그 외는 Claude 목록에서 제거한다. |

**서브커맨드 (`update`) — 수동 업데이트**: `quota-cli update`는 Go module proxy가 해석한 `@latest` 릴리스 태그(`internal/update.Latest`)를 현재 바이너리 버전과 비교해, 같으면 "이미 최신"을 출력하고, 다르면 `go install <module>/cmd/quota-cli@<latest>`로 설치한 뒤 설치 경로와 버전을 출력한다.
- **수동 전용**: 어떤 조회 경로도 업데이트를 부수 효과로 일으키지 않는다. quota-cli는 quota-bar를 건드리지 않는다(역도 같다).
- 로컬 빌드에서 실행하면 항상 최신 릴리스로 교체된다: 로컬 빌드의 버전은 릴리스 태그와 일치하지 않는 형태(`v0.9.0+dirty`, 커밋 해시, `dev` 등)라 비교가 불일치한다 — 의도된 동작이며, 최신 태그보다 앞선 dev 빌드라면 사실상 다운그레이드가 되지만 `업데이트: X → Y` 출력에 그대로 드러난다.
- 요구사항: PATH에 `go` 필요. 제약: 방금 push한 태그는 proxy 캐시로 몇 분 늦게 보일 수 있다.

- `account` 첫 인자가 아니면 조회 모드로 동작한다(기존 동작).
- 검증 규칙은 조회 시 `config.json`을 읽는 규칙과 동일하다(같은 형식/중복 규칙). Claude는 `^claude-\d+$`, Codex는 `^codex-\d+$`.

**동작**:
1. `~/.config/quota/config.json`에서 추가 Claude/Codex 계정 목록을 읽는다 (파일 없거나 목록 비면 각 기본 계정만).
2. 기본 Claude 계정 + 추가 Claude 계정을 각각 `claude.GetQuotaForConfigDir`로 조회
3. 기본 Codex 계정 + 추가 Codex 계정을 각각 `codex.GetQuotaForHome`로 조회 (기본 계정은 `home=""`)
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
  ]
}
```
- `claudeAccounts` (optional): 기본 계정 외에 추가로 조회할 Claude 계정 목록.
  - `key` (string, 필수): 출력 top-level 키. **`claude-<정수>` 형식이어야 한다**(정규식 `^claude-\d+$`, 예: `claude-2`, `claude-3`). 기본 계정 `claude` 및 다른 항목과 중복 불가. (sky-ai 등 소비자는 `^claude-?\d+$`로 추가 provider를 인식한다.)
  - `configDir` (string, 필수): 해당 계정의 Claude config 디렉터리. `~`는 홈으로 확장된다. 유저가 직접 지정하며, 외부 도구의 설정을 참조하지 않는다. 서로 다른 계정은 서로 다른 `configDir`를 가리켜야 한다.
- `codexAccounts` (optional): 기본 계정 외에 추가로 조회할 Codex 계정 목록. `claudeAccounts`와 **대칭 구조**다.
  - `key` (string, 필수): 출력 top-level 키. **`codex-<정수>` 형식이어야 한다**(정규식 `^codex-\d+$`, 예: `codex-2`). 기본 계정 `codex` 및 다른 항목과 중복 불가.
  - `home` (string, 필수): 해당 계정의 `CODEX_HOME` 디렉터리. `~`는 홈으로 확장된다. 서로 다른 계정은 서로 다른 `home`을 가리켜야 한다. 각 home에는 **동일/다른 계정을 별도 로그인**해 두어야 한다(인증 파일 복사가 아니라 `CODEX_HOME=<home> codex login`).
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
5. **refresh = 각 Claude 계정 `claude.GetQuotaForConfigDir(timeout, configDir)` (기본 `configDir=""`) + 각 Codex 계정 `codex.GetQuotaForHome(timeout, home)` (기본 `home=""`)를 병렬 호출** (내부 패키지)
6. 결과를 메뉴 항목에 표시

**계정 목록 확정 시점 (중요)**: systray는 런타임에 메뉴 항목을 추가·제거할 수 없다. 따라서 계정 수와 메뉴 레이아웃은 **onReady 시작 시점의 config로 고정**된다. `config.json`을 편집해 계정을 추가/제거하면 **quota-bar를 재시작**해야 반영된다.

**수동 업데이트 메뉴 ("Check for Updates…")**: 클릭 시 quota-cli의 `update`와 같은 비교·설치 흐름(`internal/update`)을 수행하고, 설치 성공 시 **설치된 바이너리를 `syscall.Exec`으로 자기 자리에서 재실행**한다(PID 유지 — launchd 추적 유지, pid 락은 `FD_CLOEXEC`라 새 이미지가 재획득).
- **refresh 게이트는 exec 직전에만 획득한다**(최대 3분 대기) — 조회 중 exec하면 진행 중 tmux 프로브 세션이 claude 프로세스째 고아가 되기 때문이고, 확인·설치는 게이트가 필요 없다(설치는 파일 쓰기일 뿐). 성공 경로에서는 게이트를 반환하지 않는다(프로세스 이미지가 교체되므로).
- **게이트를 쥔 동안 systray 호출을 하지 않는다.** systray의 모든 메뉴 조작은 Cocoa 메인 스레드 동기 디스패치(`waitUntilDone:YES`)라서 메뉴 닫힘과 경합하면 영구 블로킹될 수 있다(실사고 발생). 그래서 (1) 클릭 직후 첫 조작 전에 짧게 대기하고, (2) 모든 상태 전이는 **로그를 먼저 남긴 뒤** 화면에 그리며(wedge가 나도 침묵 불가), (3) 워치독이 10분 내 미완료 흐름을 로그로 알린다. 조작이 wedge되어도 게이트가 없으므로 refresh 루프는 계속 돈다.
- **버튼과 상태는 분리된 표면이다**: 버튼 제목은 항상 "Check for Updates…"로 불변이며 클릭의 의미는 언제나 "지금 확인" 하나다. 진행 상태와 결과(최신임/실패)는 버튼 아래 별도 비활성 상태 행에 표시하고, 마지막 결과는 다음 확인 때까지 유지된다(첫 사용 전에는 상태 행 숨김). 흐름이 도는 동안 버튼은 비활성화된다 — 클릭이 조용히 무시되는 상태를 만들지 않는다. 실패는 로그에도 남긴다.
- 수동 전용: 자동 체크·자동 설치는 없다.
- 알려진 제약: go-install 경로가 아닌 바이너리(예: dev 체크아웃 빌드)를 Start at Login으로 등록한 경우, exec는 go-install 경로의 새 바이너리로 갈아타지만 launchd plist는 등록 시점의 옛 경로를 그대로 가리키므로 다음 launchd 재시작 때 옛 바이너리로 돌아갈 수 있다. `go install`로 설치한 표준 경로에서는 경로가 일치해 문제없다.

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
- `Reset as clock time` 체크 시 각 항목의 괄호를 남은시간 대신 절대 시각으로 표시한다: `Weekly 90% (Jul 6 15:04)`. 절대 시각을 모르는 항목(`resetsAt` 없음)은 남은시간으로 유지한다.

**Codex 초기화권 (Reset credits 행)**:
- 라벨은 `Reset credits`다 — Codex 공식 표현(응답 필드 `rateLimitResetCredits`)을 따르며 "초기화권"(초기화=reset, 권=credit) 의미를 담는다.
- Codex `resetCredits`(초기화권)가 있으면 **해당 Codex 계정 섹션마다** `Reset credits: N` 부모 행을 두고, 각 초기화권 만료를 **서브메뉴**로 나열한다(만료 임박순). `N`은 사용 가능 수(`= 나열한 자식 수`), 부모의 괄호는 가장 임박한 만료다(수식어 없이 시각만). 계정별로 부모·슬롯을 각각 미리 만든다.
- **표시 전용**이다: 체크박스 아님, 상단 % 바에 넣지 않는다. 부모·자식 모두 enable 상태로 두어(가독성 위해 — disable 회색 텍스트를 피한다) 클릭은 아무 동작 없이 무시된다(정보 행). 부모 행에는 툴팁을 달지 않는다(뷰를 가림).
- 부모/자식 시각도 **`Reset as clock time` 토글을 그대로 따른다** — off면 남은시간(`1d 0h`), on이면 절대 시각(`Jul 12 10:42`). 절대 시각을 모르면 남은시간으로 유지하고, 남은시간·절대 시각 모두 없으면 grant 제목으로 대체한다.
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
- `showResetTime` (bool, optional, 기본 false): true면 각 항목의 리셋을 남은시간 대신 절대 시각(`FormatResetAt`, 예: `Jul 6 15:04`)으로 표시. Codex 초기화권(Reset credits 부모·서브메뉴)의 만료 시각도 동일하게 따른다. 메뉴의 `Reset as clock time` 체크로 토글하며, 즉시 저장 후 재조회 없이 메뉴만 다시 그린다(초기화권 행 포함). 상단 바(퍼센트 전용)에는 영향 없다.
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

## Internal 패키지 사양

### internal/claude

**함수**:
- `GetQuota(timeout time.Duration) (map[string]any, error)` — 기본 계정 조회 (config-dir 미지정)
- `GetQuotaForConfigDir(timeout time.Duration, configDir string) (map[string]any, error)` — 지정한 `CLAUDE_CONFIG_DIR` 계정 조회. `configDir`가 빈 문자열이면 `GetQuota`와 동일.
- `WindowKeys() []string` — 이 provider가 낼 수 있는 창 key 전부(표시 순서). 슬롯을 미리 만들어야 하는 소비자(quota-bar)가 열거한다.

- 내장 tmux 자동화로 Claude CLI에서 quota 조회. 두 함수는 동일한 조회 로직을 공유하며 config-dir 주입 여부만 다르다.

**tmux 자동화 흐름** (내장):
1. `tmux new-session -d -P -F '#{session_id}' -s <session> -x 120 -y 40 -c <safeDir> env -u CLAUDECODE -u ANTHROPIC_AUTH_TOKEN -u ANTHROPIC_BASE_URL [CLAUDE_CONFIG_DIR=<configDir>] claude`
   - `session` = `quota-{pid}-{랜덤 nonce}` — fetch마다 유일한 이름. 이름은 정보용이고, 생성 시 `-P -F`로 받은 **세션 ID(`$n`)를 이후 모든 tmux 명령의 타겟으로 사용한다.** 이름 타겟은 정확히 일치하는 세션이 없으면 tmux가 prefix 매칭으로 다른 세션(예: 병렬 조회 중인 다른 계정 세션)에 조용히 붙이므로 금지.
   - `safeDir` = `~/.config/quota` (Claude CLI가 CWD를 readdir할 때 macOS TCC 보호 폴더 접근 방지)
   - `CLAUDECODE` 제거: 중첩 세션 감지 회피
   - `ANTHROPIC_AUTH_TOKEN` / `ANTHROPIC_BASE_URL` 제거: 사용자 로그인 계정 quota를 읽도록 강제 (커스텀 엔드포인트/대체 토큰이 quota를 가로채지 않게)
   - `CLAUDE_CONFIG_DIR=<configDir>`: config-dir 지정 시에만 추가. **`-u` 옵션들 뒤, command 앞**에 둔다 (macOS `env`는 옵션이 `NAME=VALUE` 할당보다 먼저 와야 하므로 순서 고정). 명령줄에 직접 주입해야 tmux 서버 환경 상속과 무관하게 전달된다.
2. 스플래시(`Claude Code`) 등장까지 폴링 → Enter (초기 prompt 해제)
3. `/usage` 입력 → Enter
4. `% used` 가 보일 때까지 폴링 (또는 `Error:` 검출 시 즉시 진행). 게이트는 데이터 자체이지 대화상자 장식 문구가 아니다 — footer 문구는 Claude 버전마다 바뀐다 (v2.1.212에서 "Esc to cancel" 삭제됨). 행 전부가 그려졌는지는 5단계 settle이 판정한다.
5. settle 대기 — `/usage` 화면 행이 비동기로 그려지므로 최소 2초 대기 후 500ms 간격으로 재캡처하며,
   연속 두 캡처가 동일하고 **화면이 구조적으로 완결**(모든 사용률 바 아래에 Resets 줄 존재)일 때까지 폴링한다
   (최대 8초 상한 — 상한 도달 시 마지막 캡처를 그대로 파싱하므로, Resets 줄 없는 미래 레이아웃도 데이터 손실 없이 처리).
   settle 중 캡처가 실패했고 마지막 캡처가 미완결이면, 부분 데이터를 조용히 내보내는 대신 에러로 실패한다.
6. 마지막 캡처 결과를 파싱에 사용
7. ANSI 코드 제거 → `parseCaptured()` 로 파싱
8. Escape → `/exit` → Enter → 클린업 — 자기 **세션 ID**만 타겟으로 pane 프로세스 트리와 세션을 종료한다. 세션이 이미 없으면 아무것도 죽이지 않는다 (이름 타겟 클린업은 prefix 매칭으로 다른 계정 세션을 죽일 수 있으므로 금지).

**파싱 로직** (`parseCaptured`) — 줄 단위 파싱, 공통 `windows` 목록(화면 순서)으로 반환:
- `\d+% used` 를 포함한 줄을 모두 찾음 (진행 바 줄)
- 화면 텍스트 결정 (순서대로):
  1. 같은 줄에서 매치 앞부분의 바 문자(U+2580–U+259F)와 공백을 제거한 나머지 텍스트
  2. 없으면 위쪽으로 가장 가까운 비어있지 않은 줄 (최대 3줄, 바/Resets 줄이면 무효)
- **key(구조적 슬롯 식별자) 분류**:
  - `Current session` 포함 → `session`
  - `all models` 포함 → `weekly_all`
  - 그 외(모델별 행) → `extra_N` (화면 순서, 라벨 중복 제거, `extraSlots`까지)
- **label**: 위 화면 텍스트에서 `windowLabel`로 도출한다(하드코딩 어휘 없음 — 데이터 모델의 Claude 절 참조)
- resets: 진행 바 줄 아래쪽으로 가장 가까운 비어있지 않은 줄이 `Resets ...` 형식이면 추출. 상대시간(`resetsIn`)으로 정규화하고, 절대표기를 파싱할 수 있으면 절대 리셋 시각(`resetsAt`, `time.Time`)도 함께 채운다 (`parseReset`)
- 일부 행이 화면에 없거나 매칭이 일부만 되어도 매치된 항목만 반환 (부분 결과 허용)
- 하단 "What's contributing" 섹션의 `NN% of ...` 텍스트는 `% used` 패턴이 아니므로 매치되지 않는다

### internal/codex

**함수**:
- `GetQuota(timeout time.Duration) (map[string]any, error)` — 기본 계정 조회 (CODEX_HOME 미주입)
- `GetQuotaForHome(timeout time.Duration, codexHome string) (map[string]any, error)` — 지정한 `CODEX_HOME` 계정 조회. `codexHome`가 빈 문자열이면 `GetQuota`와 동일. `codexHome`는 이미 확장된 절대경로여야 하며, 호출자가 `config.ExpandTilde`로 확장해 넘긴다(Claude `GetQuotaForConfigDir`와 대칭).

**동작**:
1. `codex app-server` 프로세스를 시작 (stdin/stdout pipe). `codexHome`가 비어있지 않으면 `CODEX_HOME=<codexHome>`을 프로세스 환경 끝에 추가해 주입한다(마지막 값이 우선하므로 상속된 `CODEX_HOME`을 덮어씀). 빈 문자열이면 주입하지 않고 프로세스 환경을 그대로 상속한다.
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
- `FormatResetAt(t time.Time) string` — 절대 리셋 시각을 `Jan 2 15:04`(로컬 tz, 24시간제, **날짜 항상 포함**, 연도 생략)로 포맷. quota-bar와 공유하는 단일 포맷 소스.

- Claude, Codex 데이터를 사람이 읽을 수 있는 텍스트로 변환
- 계정별 섹션: Claude는 `claude`+`claude-N`, Codex는 `codex`+`codex-N`을 각각 기본 먼저·숫자 오름차순으로 헤더(`Claude`/`Claude 2`, `Codex`/`Codex 2`)와 함께 렌더한다. Claude 그룹들 뒤에 Codex 그룹들이 온다.
- **provider 분기 없음**: 각 그룹은 자기 `windows` 목록을 순회해 각 항목을 **그 항목의 `label`로** 렌더한다(목록이 이미 정식 순서). render는 어떤 창 이름도 갖지 않으므로, 새 창 종류·기간 변경·새 provider도 그대로 표시된다.
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
- `go`: 수동 업데이트(`quota-cli update`, quota-bar 업데이트 메뉴)에만 필요

---

## 현재 알려진 이슈

없음.
