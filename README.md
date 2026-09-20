# quota

Claude Code와 Codex CLI의 사용량(quota)을 조회하는 Go 도구.

## 바이너리

| 이름 | 설명 |
|------|------|
| `quota-cli` | CLI. quota 출력, quota 기반 비대화형 Claude/Codex 실행/추천, 세션 로그 검색, agent hook 정책 관리 |
| `quota-bar` | macOS 메뉴바 앱. 주기적으로 quota 갱신 표시 |

## 설치

```bash
go install github.com/sky1core/quota/cmd/quota-cli@latest
go install github.com/sky1core/quota/cmd/quota-bar@latest
```

이후에는 한쪽에서 수동 업데이트하면 표준 Go 설치 디렉터리에 설치된 두 실행 파일을 같은 릴리스로 맞춘다. CLI만 설치돼 있으면 메뉴바를 새로 설치하지 않는다.

- `quota-cli update` — CLI와 이미 설치된 메뉴바를 함께 갱신. 실행 중인 메뉴바의 재시작은 별도로 안내한다.
- quota-bar 메뉴의 **Check for Updates…** — 메뉴바와 이미 설치된 CLI를 함께 갱신하고, 실행 중인 메뉴바 버전이 바뀌면 자동 재시작한다.

버전이 다른 대상 바이너리만 모두 준비한 뒤 교체하므로 빌드 실패 시 기존 설치본은 유지된다. CLI의 Ctrl-C(SIGINT)·SIGTERM도 취소·복구 절차를 거쳐 처리한다. 교체 중 실패하면 복구를 시도하고 복구 실패도 보고한다. 여러 파일의 교체가 한 순간에 이루어지는 것은 아니다. 설치 디렉터리에는 읽기·쓰기·탐색 권한이 필요하고, 기존 실행 파일의 하드링크 백업을 운영체제가 허용해야 한다. 설치 경로는 절대경로여야 하며 심볼릭 링크·다른 OS/아키텍처의 설치본은 교체를 거부한다.

개발 중 최신 commit 또는 특정 commit/tag/branch를 설치하려면 quota-bar **Settings… → Update**에서 설치 기준을 바꾼다. 같은 값은 `~/.config/quota/config.json`의 `update.ref`에 저장되며 `quota-cli update`와 메뉴바 업데이트가 함께 사용한다. 값이 없으면 기본값은 `@latest` 릴리스다. 값이 있으면 Go module proxy 대신 직접 VCS 조회로 해석·설치한다.

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
- **기본 계정은 quota 기본값인 `~/.claude`와 `~/.codex`로 고정된다.** 호출한 셸이나 에이전트의 `CLAUDE_CONFIG_DIR`/`CODEX_HOME`은 기본 계정 선택에 쓰지 않는다. 다른 계정을 함께 보려면 추가 계정으로 등록해야 하며, 심볼릭 링크로 같은 위치를 가리키는 경우도 중복으로 판단한다.

등록하면 `quota-cli`가 기본 계정과 추가 계정을 함께 조회해 각각 `claude`/`claude-2`, `codex`/`codex-2` … 로
출력한다. 설정은 `~/.config/quota/config.json`에 저장되며, 직접 편집해도 된다:

```json
{
  "claudeAccounts": [ { "key": "claude-2", "configDir": "~/.claude-2" } ],
  "codexAccounts":  [ { "key": "codex-2",  "home": "~/.codex-alt" } ],
  "update": { "ref": "main" },
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

# provider 생략: 모델·effort 두 후보 중 quota에 따라 하나 실행
quota-cli exec-prompt \
  --model claude-opus-4-8:high \
  --model gpt-6-astra:high \
  -- "프롬프트"
```

자동 실행은 `--model 모델명:effort`를 정확히 두 번 받으며 두 후보의 순서는 무관하다.
등록된 Codex 계정들의 모델 목록에 정확히 일치하면 Codex, 없으면 Claude로 분류한다.
두 후보는 각 provider 하나씩이어야 한다. 목록 조회 실패는 Claude로 분류하지 않고 오류로 처리한다.
effort는 `low`, `medium`, `high`, `xhigh`, `max`를 받으며 `ultra`는 허용하지 않는다.
Codex 계정은 해당 모델·effort를 목록에 제공할 때만 후보가 된다. Claude는 지정값을 그대로 전달한다.
분류는 모델 지원이나 effort의 실제 적용을 보장하지 않으며, CLI 실행 실패 후 다른 모델로 재시도하지 않는다.
자동 실행은 `--` 뒤 프롬프트 하나와 stdin을 전달한다. provider 전용 옵션이 필요하면 `--agent`를 명시한다.
읽기 중심 리뷰에는 `--` 앞에 `--read-only`를 추가한다. Claude는 `Read·Glob·Grep`만 제공하고 MCP 도구를 차단하므로 셸·테스트 실행은 불가능하다. Codex는 `--sandbox read-only`로 실행하며 기존 허용·금지 규칙을 유지한다. [Codex의 `allow` 규칙](https://learn.chatgpt.com/docs/agent-configuration/rules)으로 사전 허용된 명령은 샌드박스 밖에서 실행될 수 있으므로, 모든 명령의 쓰기를 금지하는 옵션은 아니다. 이 옵션은 provider별 실행 제한을 지정하며, 양쪽의 동일한 OS 격리나 기존 hook·외부 연동 전체의 부작용 차단을 보장하지 않는다. 옵션을 생략하면 기존 동작을 유지한다.

등록된 같은 provider 계정들의 75초 공유 캐시를 우선 사용하고, 필요한 계정만 quota를 실측한다.
신규 작업은 **5시간 quota 창이 있으면 잔여량이 25% 이상**이어야 배정한다. Claude의 `session`, Codex의
`windowMins == 300`으로 판정하며, 정상 응답에 주간 창만 있으면 주간 기준으로 배정한다.
응답에 있는 집계 창의 사용량·기간을 읽지 못하면 `windowErrors`로 구분해 배정에서 제외한다.
조회 실패나 적용할 quota 창이 없는 계정도 제외한다.
적용되는 quota 창이 계정별 `minLeftPct` 미만인 계정은 후보에서 제외한다. 기본값은 5%이며,
이 보존분은 25% 진입 기준과 별개다. 선택 점수는 남은 quota에서 `minLeftPct`를 뺀 여유분이다. 장기 quota를 짧은 quota보다 우선하며,
여유분이 리셋까지 남은 시간에 비해 많은 계정을 먼저 쓴다.
provider를 명시한 Claude 호출은 요청 모델값과 실제 추가 quota row label이 맞을 때만 그 row를 본다. 현재 Opus처럼 전용 row가
없는 모델은 별도 quota를 가정하지 않고 `weekly_all`/`session`으로 비교한다. Fable처럼 해당 모델
quota의 남은 비율을 읽을 수 있으면 그 계정에는 하한선을 적용한다. 살아남은 모든 후보가 남은 비율을
읽을 수 있는 해당 모델 quota를 갖고 있을 때만 그 quota를 우선 비교하고, 일부 후보에만 있으면
`weekly_all`/`session`으로 비교한다. 선택이 끝나면 나머지 인자와 stdin/stdout/stderr, 종료 상태는 각각 `claude -p`와 `codex exec`에 그대로 전달된다.
자동 실행에서도 Claude 모델별 하한선은 적용하지만, 순위는 모든 자격 충족 계정에 공통인 기간의 집계 quota를 긴 순서로 비교한다. 공통 기간이 없으면 오류로 종료한다.
대화형 실행은 지원하지 않는다.

#### quota 기반 agent 선택

```bash
quota-cli select-agent
quota-cli select-agent --agent=claude --model=fable
quota-cli select-agent --json
```

등록된 Claude/Codex 계정 전체를 75초 공유 캐시 기준으로 비교해 어느 provider/account를 쓸지
선택한다. 실제 프롬프트는 실행하지 않고, 선택된 계정의 실행 prefix(`claude -p` 또는 `codex exec`),
추가 계정에 필요한 환경 변수, 현재 셸에서 제거해야 할 override 환경 변수 이름을 출력한다.
`exec-prompt`와 같은 5시간 잔여량 25% 진입 기준과 `minLeftPct` 설정을 적용한다.
기본 통합 모드는 모델별 Claude row를 보지 않으며, 모델별 row는 `--agent=claude --model=...`에서만
적용한다.

#### 모델·effort 목록 캐시

```bash
quota-cli models --agent=all --json
quota-cli models refresh --account=codex
```

등록된 계정별 CLI 메타데이터에서 모델 ID, alias 해석값, effort 지원 정보를 조회한다.
`~/.config/quota/model-cache/`에 조회 성공 시각과 CLI 버전을 함께 저장한다.
기본 계정의 모델 조회도 quota 기본 계정 디렉터리로 cache identity를 고정한다. 다만 기본 Claude 실행은 상속된 `CLAUDE_CONFIG_DIR`를 제거하고 Claude의 builtin default 계정을 사용하며, Codex와 추가 Claude 계정은 확정된 계정 경로를 자식 환경에 명시한다. 호출 환경의 `CLAUDE_CONFIG_DIR`·`CODEX_HOME`은 조회 대상이나 캐시 identity를 바꾸지 않는다.
모델 캐시는 확정된 계정 경로와 자식 프로세스에 전달한 계정 환경을 함께 구분한다.
provider를 명시한 `exec-prompt`와 `select-agent`는 선택된 계정의 캐시가 없거나 3시간이 지났거나 CLI 버전이 바뀌면
갱신한다. `models refresh`는 즉시 다시 조회한다. 갱신 실패 시 오래된 목록으로 진행하지 않고 오류를 반환한다.
자동 라우팅은 분류 전에 Codex 계정들의 캐시만 확인한다. 조회 시각은 CLI 응답을 받은 시각이며,
CLI 자체 캐시가 사용될 수 있으므로 서버 갱신 시각을 뜻하지 않는다. 수동 갱신도 CLI 재조회다.
목록에 없는 모델이나 누락된 effort 정보는 미확인이다. 이 목록은 모든 실행 가능한 모델을 보장하지 않으며,
provider를 명시한 `exec-prompt`의 모델·effort 인자를 차단하거나 수정하지 않는다.

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

#### Agent hook 정책 관리

```bash
quota-cli agent hooks init --preset=github-history-guard
quota-cli agent hooks plan
quota-cli agent hooks apply
quota-cli agent hooks verify
quota-cli agent hooks doctor
```

`agent hooks`는 Claude Code와 Codex CLI의 실행 전 hook이 함께 호출할 단일 정책을 관리한다.
정책 파일은 기본적으로 `~/.config/quota/agent-hooks.d/*.json`에 저장된다. 사용자는 정책을 한 번만
작성하고, `quota-cli`가 Claude/Codex hook 설정으로 렌더링한다.

기본 preset `github-history-guard`의 Git/GitHub 보호 범위는 **원격 코드·브랜치·태그·저장소 설정 변경**이다.
`remote-code-ref-mutation`은 push, PR merge·자동 merge, 원격 ref 생성·삭제, 저장소 공개 범위·권한·보안/자동화 설정 변경 등을 차단한다.
로컬 commit/amend/merge/rebase, 브랜치·태그 조작, 조회, push 없는 PR 생성과 협업 메타데이터 작업은 허용한다.
`gh api`는 요청의 실제 대상을 판정하며, 릴리스 정보·첨부물과 태그 변경도 구분한다.
`gh stack link <number> <number>`만 허용하는 형식 제한은 별도로 유지한다.
Git/GitHub의 추가 제한은 대상·조건·사유·영향을 명시해 승인받고 별도 규칙으로 설정한다.
기본 원격 보호를 켰다는 이유로 로컬 작업 제한까지 함께 활성화하지 않는다.

`github-collaboration-metadata`는 협업 작업의 허용 대조군이며, `local-system-secret-safety`의 별도 안전 규칙도 유지한다.
실행 내용이 보이지 않는 간접 실행은 판정불가 사유와 함께 차단한다. 명령 문자열의 검색·출력은 실행으로 취급하지 않는다.
이미 저장된 정책은 바이너리 업데이트로 덮어쓰지 않는다. 새 기본값을 적용하려면 기존 사용자 규칙과 대조한 뒤
`init --preset=github-history-guard --force`로 정책을 교체하고 `verify`로 허용·차단 결과를 확인한다.
`list`/`plan`은 그룹 이름과 설명을 보여주고, `verify` 출력은 내장 테스트 이름 앞에 그룹 이름을 붙여 어느 그룹의 동작을 확인하는지 보여준다.

`plan`은 설치될 hook 위치와 명령을 보여주고 파일을 수정하지 않는다. `plan`/`doctor`/`apply`는
`--runtime=claude|codex|all`, `--binary <quota-cli-path>`를 받을 수 있다. `apply`는 현재 사용자 계정의
Claude/Codex hook 설정 파일을 백업한 뒤 managed hook을 설치한다. 적용 전에 enabled 정책이 최소 1개 있어야
하며, `--policy-dir`를 지정하면 hook 명령도 같은 정책 디렉터리를 사용한다. `--binary`와 `--policy-dir`의
상대 경로는 설치 시 절대 경로로 고정된다. `verify`는 정책에 내장된
positive/negative 케이스를 evaluator로 검사한다. `doctor`는 evaluator 설치 여부와 실행 파일, 사용자 설정에 드러난 hook 비활성화·조건부·비동기 등의 방해 조건을 검사한다.
JSON의 `present`는 설치 여부이고, 방해 조건은 `reasons`, 읽기·파싱 오류는 `error`로 보고하며 진단 실패 시 exit 1을 반환한다.
`plan`에도 같은 진단이 표시된다. `apply`는 사용자 비활성화 설정을 변경하지 않으며, 저장 후 진단에 실패하면 저장 사실과 원인을 알린다. 여러 런타임 적용은 각각 처리하고 결과를 함께 보고한다.
프로젝트·관리자 설정 전체나 실제 hook 실행을 검증하는 것은 아니다. 정적으로 볼 수 없는 shell interpreter stdin/script/startup 파일 실행과
interactive/login shell startup 실행은 차단한다. 각 agent가 새 hook 설정을 신뢰·재로드해야 실제 실행 전 차단이 적용된다.

hook이 호출하는 내부 명령은 다음 형태다:

```bash
quota-cli agent hooks eval --runtime=claude
quota-cli agent hooks eval --runtime=codex
```

#### Agent instructions — 공용·개인 지침 전달

`agent instructions`는 공용 `AGENTS.md`와 개인 `AGENTS.local.md`를 Claude Code와 Codex CLI가 native로 읽게 한다.
실행부는 quota-cli에 포함되며 별도 스크립트·hook spec이 필요하지 않다.
Git 2.36 이상, Claude Code 2.1.232 이상·Codex CLI 0.154.0 이상을 대상으로 한다.

**한 번만 설정**

```bash
quota-cli agent instructions setup --dry-run
quota-cli agent instructions setup
quota-cli agent instructions status
```

`setup`은 현재 계정에만 적용한다. Claude `settings.json`의 SessionStart·WorktreeCreate·WorktreeRemove와
Codex `hooks.json`의 SessionStart에 고정된 준비 명령(`_prepare`)을 설치하고, Codex hook에는 `additionalContextLimit=0`을 둔다.
이전 버전이 설치한 지침 hook은 교체·제거한다.
Codex가 있으면 설치한 quota hook의 현재 native hook hash만 `config.toml`의 trust state에 동기화하고,
무관한 hook·설정·신뢰 상태는 보존한다. 전역 git ignore 파일(`core.excludesFile`, 없으면 `~/.config/git/ignore`)에
`AGENTS.override.md`, `.claude/AGENTS.md`, `.claude/CLAUDE.md`가 없으면 `# quota-cli agent instructions (managed)` 표식 아래에 추가한다.
이미 있는 줄은 그대로 두고 관리 대상으로 삼지 않는다. `--no-global-ignore`를 주면 추가하지 않는다.
`--agent=claude|codex`로 한 도구만 설정할 수 있고, `--dry-run`은 같은 사전 검사와 변경 계획만 출력한다.
저장소별 준비 명령은 없다.

**저장소 준비는 세션 시작 시 자동**

공용 지침은 각 checkout의 `AGENTS.md`, 개인 지침은 primary(bare 저장소는 bare 루트)의 `AGENTS.local.md`에 작성한다.
개인 파일은 Git에서 제외한다. 세션이 시작되면 준비 hook이 그 checkout에 대해 다음을 수행한다.

- 세션 시작 디렉터리부터 상위 디렉터리까지 `CLAUDE.md`/`.claude/CLAUDE.md`/`CLAUDE.local.md`가 없으면 checkout에 `.claude/AGENTS.md`를 `@../AGENTS.local.md` 한 줄로 만든다. 이미 있는 파일은 건드리지 않는다.
- 세션 시작 디렉터리부터 상위 디렉터리까지 `CLAUDE.md`/`.claude/CLAUDE.md`가 있으면 해당 파일이 checkout의 `AGENTS.md`를 직접 import해야 호환으로 본다. 이때 개인 지침은 quota가 만든 `.claude/CLAUDE.md`의 `@../AGENTS.local.md`로 전달한다. `CLAUDE.local.md`가 있으면 `AGENTS.local.md`와 경쟁하는 로컬 지침으로 보고한다. 사용자 전역 `~/.claude/CLAUDE.md`는 이 판단에서 제외한다.
- `AGENTS.override.md`를 checkout의 `AGENTS.md`(있으면)와 primary `AGENTS.local.md`의 병합본으로 만들거나 갱신한다. Codex는 이 파일이 있으면 이것을, 없으면 `AGENTS.md`를 읽는다.
- linked worktree에는 primary `AGENTS.local.md`와 등록한 로컬 파일의 복사본을 갱신한다.
- `AGENTS.local.md`가 없으면 quota가 만든 변경 없는 생성물과 복사본을 제거한다.

생성물은 쓰기 전에 git-ignored여야 한다. ignore되지 않은 파일은 만들지 않고 이유와 필요한 ignore 줄을 세션에 알린다.
quota가 만든 파일은 기록된 해시와 권한이 일치할 때만 갱신·제거한다. 사용자가 만든 파일이나 수정한 생성물은
내용이 같아 보여도 덮어쓰거나 지우지 않는다. Git 저장소가 아닌 폴더에서는 아무것도 하지 않는다.

quota는 저장소의 `.git/` 아래에 아무것도 쓰지 않는다. 생성물 소유권과 로컬 파일 등록은
`~/.config/quota/instructions/<common-dir 해시>.json`에 소유자 전용 권한으로 저장한다.

**첫 세션 1회 전달**

새 세션(startup)에서 준비 hook이 그 호출에서 native 로딩이 이미 끝난 파일을 바꿨을 때만, 그 세션에 한해
additionalContext로 한 번 전달한다. Claude는 `.claude/AGENTS.md`, `.claude/CLAUDE.md`, 또는 linked worktree의
`AGENTS.local.md` 복사본을 그 startup에서 생성·갱신했을 때 native가 아직 읽지 못한 본문만 전달한다. 이전 로컬 bridge 제거처럼 개인 본문을 이미 native가 읽은 migration에서는 개인 본문을 다시 보내지 않는다. Codex는 `AGENTS.override.md`가 생성됐을 때 개인 본문을,
갱신됐을 때는 병합본 전체를, 로컬 지침 제거로 삭제됐을 때는 현재 `AGENTS.md` 본문을 전달한다.
대체 Claude bridge를 준비하지 못하면 이전 quota 소유 Claude bridge는 보존한다.
같은 checkout에서 준비 파일이 없던 첫 startup을 동시에 여러 개 시작하는 경우는 보장하지 않으며, 다음 새 세션부터 native 파일 상태를 따른다.
resume·compact·clear, 변경 없음, 생성 실패에는 전달하지 않는다. 그 이후는 native 로딩만 사용하며
같은 본문을 hook이나 매 턴 입력에 다시 붙이지 않는다. 지침을 바꾼 뒤 기존 세션을 재개하면 이전 본문이 컨텍스트에 남을 수 있다.

**로컬 파일 복사 목록**

```bash
quota-cli agent instructions local-file add config/example.local.json
quota-cli agent instructions local-file list
quota-cli agent instructions local-file remove config/example.local.json
```

primary 기준 상대 경로를 linked worktree 복사 목록에 등록·해제·조회한다. 8MiB 이하의 ignored·untracked 정규 파일만
복사하며 소유자 실행 권한을 보존한다. 경로 이탈·symlink·생성물 경로·`.gitignore`·대소문자와 Unicode 정규화 기준의 중복 경로는 거부한다.
등록만 하며 실제 복사는 세션 시작과 Claude WorktreeCreate가 수행한다.

**설정 확인**

```bash
quota-cli agent instructions status . --agent=all
```

`status`는 모델 호출 없이 준비 hook과 같은 평가를 쓰기 없이 수행해, 다음 준비가 생성·갱신·제거·건너뜀으로 판단할
파일과 그 이유를 보고하고 계정 연결·유효 native 설정을 검사한다. 이미 남아 있고 소유권이 유지된 생성물은 `generated:`로 표시한다.
`CLAUDE.md`나 `.claude/CLAUDE.md`가 세션 시작 디렉터리부터 상위 디렉터리까지 있으면 checkout `AGENTS.md` 직접 import가 있는 경우만 Claude 호환으로 본다.
`CLAUDE.local.md`가 경로에 있으면 `AGENTS.local.md`와 경쟁하는 로컬 지침이므로 Claude를 차단됨으로 보고한다.
Claude 설정이 AGENTS 로딩을 끄고 CLAUDE 경로가 checkout `AGENTS.md`를 가져오지 못하면 차단됨으로 보고한다.
준비되지 않았거나 확인할 수 없으면 성공으로 처리하지 않는다.
Codex는 유효 `project_doc_max_bytes` 기준으로 현재 읽히는 `AGENTS.override.md` 또는 `AGENTS.md`를 문제로 보고하고,
`AGENTS.override.md`가 있을 때 한도를 넘은 비활성 `AGENTS.md`는 경고로 보고한다.

**해제**

```bash
quota-cli agent instructions uninstall --dry-run
quota-cli agent instructions uninstall
```

계정의 준비 hook을 제거한다. 전역 ignore의 관리 블록은 남은 생성물이 커밋되지 않도록 기본적으로 유지하며,
`--remove-global-ignore`를 `--agent=all`과 함께 주면 표식 아래의 줄만 제거한다. 사용자가 직접 쓴 같은 줄은 남긴다.
저장소의 생성물은 건드리지 않으며 `status`가 남은 생성물을 보고한다.

### quota-bar

```bash
quota-bar
```

메뉴 첫 화면에 모든 계정의 쿼터를 펼쳐 표시한다. 항목을 체크하면 상단 바에 남은 %를 표시.

`quota-cli account add`로 추가 계정을 등록해 두면, quota-bar도 계정별 그룹(`Claude`, `Claude 2`, …,
`Codex`, `Codex 2`, …)으로 나눠 표시한다. quota-cli와 같은 `config.json`을 공유한다.

**Settings…**에서 표시·갱신 주기·로그인 시 실행, 계정 등록과 잔여량 하한, keepalive를 설정한다.
저장한 값은 재시작 없이 반영된다. 계정 제거는 quota 등록만 해제하며 CLI 인증·세션 파일은 보존한다.
잘못된 입력·저장 실패·편집 중 설정 충돌은 창에 표시한다.

사용자 활동에 따라 갱신 주기가 자동 조절된다:
- 활성 사용 중: 3분 (`refreshActiveMinutes`로 변경 가능)
- 10분 이상 idle: 30분 (`refreshIdleMinutes`로 변경 가능)
- 1시간 이상 idle: 정지 (복귀 시 자동 재개)

두 주기는 `quota-bar.json`에 값을 쓰면 그 값을, 없으면 위 기본값을 쓴다. 설정창에서 저장하면 즉시 반영되며, 파일을 직접 편집한 경우에는 재시작 후 반영된다.

설정 파일: `~/.config/quota/quota-bar.json`

메뉴의 **Keep session caches warm**은 기본 꺼짐이다. 켜면 평일 12:30에 PC 입력이
5분 이상 없고, 최근 50분 내 작업을 마친 뒤 살아 있는 CLI에서 대기 중인 세션만 확인한다.
상태가 불명확하거나 승인·작업 대기 중이면 보내지 않는다. 별도 세션을 재개하지 않고 기존 CLI로
짧은 메시지를 보내며, 쿼터를 사용한다. 잠자기·앱 종료로 놓친 일정은 건너뛴다.
요일·시각·시간 범위·메시지는 설정창 또는 설정 파일의 `keepalive`에서 변경할 수 있다([설정 계약](SPEC.md)).
메뉴는 응답 완료와 캐시 재사용·미적중·측정 불가를 구분하고, 툴팁에 계정별 재사용 토큰 수를 표시한다.
캐시 보존 시간과 효과는 모델에 따라 다르며, 응답 완료나 캐시 재사용 확인이 이후의 캐시 연장 보장은 아니다.
Claude는 실행 중인 CLI의 입력 소켓, Codex는 해당 세션의 입력 큐로 전달한다.
정식 릴리스의 최소 요구 버전은 Claude Code 2.1.259, Codex CLI 0.153.4이며 버전 상한은 두지 않는다.
해당 버전 이상이어도 필요한 입력 경로·상태 형식을 확인할 수 없으면 원인을 표시하고 건너뛴다.
실행 상태 확인에 `lsof`, Codex 세션 조회에 추가로 `sqlite3`가 필요하다.

"Start at Login"은 다음 로그인부터 적용된다. 현재 실행 중인 앱이나 launchd 작업을 중단하지 않는다.

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
