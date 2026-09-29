# quota

Claude Code와 Codex CLI의 멀티 에이전트·멀티 계정 사용환경을 관리하는 Go 도구.
핵심 목표 중 하나는 에이전트나 계정을 전환해도 동일한 공통 지침·스킬·정책을 사용하는 환경을 구축하는 것이다.
기본 계정과 등록된 추가 계정 모두를 대상으로 한다.

쿼터 조회·표시와 잔여량 기반 계정 선택·실행 위임도 이 목표의 일환이다.
각 에이전트·계정의 잔여량을 파악하고 작업을 배정하면서 공통 사용환경을 유지하도록 돕는다.

이 문서는 설치와 사용법을 다룬다. 동작 계약은 [SPEC.md](SPEC.md)에 있다.

## 바이너리

| 이름 | 설명 |
|------|------|
| `quota-cli` | CLI. quota 조회, quota 기반 비대화형 Claude/Codex 실행·선택, 세션 로그 검색, agent hook 정책·지침 전달·스킬 관리 |
| `quota-bar` | macOS 메뉴바 앱. 주기적으로 quota 갱신 표시 |

## 설치

```bash
go install github.com/sky1core/quota/cmd/quota-cli@latest
go install github.com/sky1core/quota/cmd/quota-bar@latest
```

이후 업데이트는 한쪽에서 실행하면 표준 Go 설치 디렉터리의 두 실행 파일을 같은 릴리스로 맞춘다. CLI만 설치돼 있으면 메뉴바를 새로 설치하지 않는다.

- `quota-cli update` — CLI와 이미 설치된 메뉴바를 함께 갱신. 실행 중인 메뉴바의 재시작은 별도로 안내한다.
- quota-bar 메뉴의 **Check for Updates…** — 메뉴바와 이미 설치된 CLI를 함께 갱신하고, 실행 중인 메뉴바 버전이 바뀌면 자동 재시작한다.

버전이 다른 대상 바이너리만 모두 준비한 뒤 교체하므로 빌드 실패 시 기존 설치본은 유지된다. CLI의 Ctrl-C(SIGINT)·SIGTERM도 취소·복구 절차를 거쳐 처리한다. 교체 중 실패하면 복구를 시도하고 복구 실패도 보고한다. 여러 파일의 교체가 한 순간에 이루어지는 것은 아니다. 설치 디렉터리에는 읽기·쓰기·탐색 권한이 필요하고, 기존 실행 파일의 하드링크 백업을 운영체제가 허용해야 한다. 설치 경로는 절대경로여야 하며 심볼릭 링크·다른 OS/아키텍처의 설치본은 교체를 거부한다.

특정 commit/tag/branch를 설치하려면 quota-bar **Settings… → Update**에서 설치 기준을 바꾸거나 `~/.config/quota/config.json`의 `update.ref`에 값을 쓴다. 값이 없으면 `@latest` 릴리스를 설치하고, 값이 있으면 Go module proxy 대신 직접 VCS 조회로 해석·설치한다.

### 시스템 요구사항

- `quota-cli` 지원 OS — macOS 또는 Linux. Windows는 지원하지 않는다.
- `claude` CLI — PATH 또는 `~/.local/bin/claude`. `claude -p "/usage"`로 사용량을 조회하므로 그 명령을 지원해야 한다. 지원하지 않으면 Claude quota 조회만 실패한다.
- `codex` CLI — PATH.
- `go` — 수동 업데이트에만 필요하다.

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

자동 실행은 `--model 모델명:effort`를 정확히 두 번 받는다. 등록된 Codex 계정의 모델 목록에 있으면 Codex, 없으면 Claude로 분류하며 두 후보는 provider가 달라야 한다. 목록 조회 실패는 오류로 종료한다. Codex 계정은 자기 목록에 요청 모델과 effort가 있을 때만 후보가 된다. effort는 `low`, `medium`, `high`, `xhigh`, `max`를 받는다. 분류는 모델 지원이나 effort의 실제 적용을 보장하지 않으며, 실행 실패 후 다른 모델로 재시도하지 않는다. `--` 뒤 프롬프트 하나와 stdin을 전달하고, provider 전용 옵션이 필요하면 `--agent`를 명시한다.

자동 실행의 `--read-only`는 읽기 중심 실행 제한이다. Claude는 `Read·Glob·Grep`만 제공하고 MCP 도구를 차단하며, Codex는 `--sandbox read-only`로 실행한다. [Codex의 `allow` 규칙](https://learn.chatgpt.com/docs/agent-configuration/rules)으로 사전 허용된 명령은 샌드박스 밖에서 실행될 수 있으므로 모든 쓰기를 막는 옵션은 아니다. 양쪽의 동일한 OS 격리나 hook·외부 연동의 부작용 차단도 보장하지 않는다.

계정 선택은 같은 provider 계정들의 75초 공유 캐시를 우선 사용하고 필요한 계정만 실측한다. 5시간 quota 창이 있으면 잔여량 25% 이상이어야 배정하고, 적용되는 창이 계정별 `minLeftPct`(기본 5%) 미만인 계정과 조회 실패·적용할 창이 없는 계정은 제외한다. 선택 뒤 나머지 인자와 stdin/stdout/stderr, 종료 상태는 `claude -p`·`codex exec`에 그대로 전달된다. 대화형 실행은 지원하지 않는다. 모델별 quota 비교와 순위 규칙은 [SPEC의 `exec-prompt` 동작](SPEC.md)에 있다.

위임 worktree의 `AGENTS.md`는 실행 직전에 primary 원본과 같게 만든다. 대상은 루트와 하위 폴더 전체의 미추적 파일(ignore 포함)이고, 추적 파일은 checkout에 맡기며 원본에 없는 파일은 만들거나 지우지 않는다. 준비에 실패하면 실행하지 않는다. `--worktree`처럼 실행 뒤 만들어지는 worktree는 준비할 수 없으므로 primary에 미추적 `AGENTS.md`가 있으면 실행하지 않는다. worktree를 먼저 만들고 그 경로로 위임한다. `AGENTS.local.md`는 세션 시작 hook이 primary 원본을 읽으므로 복사하지 않는다.

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

등록된 계정별 CLI 메타데이터에서 모델 ID, alias 해석값, effort 지원 정보를 조회해 `~/.config/quota/model-cache/`에 저장한다.
`models`와 자동 라우팅은 캐시가 없거나 3시간이 지났거나 CLI 버전이 바뀌면 갱신하고, `models refresh`는 즉시 다시 조회한다.
갱신 실패 시 오래된 목록으로 진행하지 않고 오류를 반환한다. provider를 명시한 `exec-prompt`와 `select-agent`는 이 캐시를 쓰지 않는다.
목록에 없는 모델이나 누락된 effort 정보는 미확인이며, provider를 명시한 `exec-prompt`의 모델·effort 인자를 차단하거나 수정하지 않는다.
호출 환경의 `CLAUDE_CONFIG_DIR`·`CODEX_HOME`은 조회 대상을 바꾸지 않는다.

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
정책 파일은 `~/.config/quota/agent-hooks.d/*.json`에 저장되고, `quota-cli`가 Claude/Codex hook 설정으로 렌더링한다.

기본 preset `github-history-guard`는 **원격 코드·브랜치·태그·저장소 설정 변경**을 차단한다.
push, PR merge·자동 merge, 원격 ref 생성·삭제, 저장소 공개 범위·권한·보안/자동화 설정 변경이 대상이다.
로컬 commit/amend/merge/rebase, 브랜치·태그 조작, 조회, push 없는 PR 생성과 협업 메타데이터 작업은 허용한다.
실행 내용이 보이지 않는 간접 실행은 판정불가 사유와 함께 차단한다.
추가 제한은 별도 규칙으로 설정하며, 기본 원격 보호가 로컬 작업 제한을 함께 켜지 않는다.
명령별 판정 범위는 [SPEC의 `agent hooks` 정책](SPEC.md)에 있다.

- `init`은 정책을 생성한다. 이미 저장된 정책은 바이너리 업데이트로 덮어쓰지 않으며, 새 기본값을 적용하려면 `init --force`로 교체하고 `verify`로 확인한다.
- `plan`은 설치될 hook 위치와 명령을 보여주고 파일을 수정하지 않는다.
- `apply`는 선택한 런타임의 기본 계정과 등록된 추가 계정의 hook 설정을 백업한 뒤 managed hook을 설치한다. enabled 정책이 최소 1개 있어야 한다.
- `verify`는 정책에 내장된 허용·차단 케이스를 검사한다.
- `doctor`는 evaluator 설치 여부와 hook 비활성화·조건부·비동기 등 방해 조건을 검사하며, 진단 실패 시 exit 1을 반환한다.

`plan`/`apply`/`doctor`는 `--runtime=claude|codex|all`, `--binary <quota-cli-path>`, `--policy-dir <dir>`를 받으며 상대 경로는 설치 시 절대 경로로 고정된다.
실제 hook 실행은 검증하지 않으며, 각 agent가 새 hook 설정을 신뢰·재로드해야 차단이 적용된다.
hook이 호출하는 내부 명령은 `quota-cli agent hooks eval --runtime=claude|codex`다.

#### Agent instructions — 개인 지침 전달

역할은 셋으로 나뉜다. 공용 `AGENTS.md`는 각 worktree의 파일을 Claude Code와 Codex CLI가 native로 읽는다. 개인 `AGENTS.local.md`는 각 계정의 세션 시작 hook이 primary 루트(bare 저장소는 bare 루트)의 원본을 전문으로 전달한다. 위임 worktree의 `AGENTS.md` 준비는 `exec-prompt`가 실행 직전에 맡는다. 세션 시작 hook은 `AGENTS.md`를 복사·주입하지 않고, quota는 CLAUDE 파일이나 Git 설정을 변경하지 않는다.

**한 번만 설정**

```bash
quota-cli agent instructions setup --dry-run
quota-cli agent instructions setup
quota-cli agent instructions status
```

`setup`은 선택한 런타임의 기본 계정과 등록된 추가 계정 모두에 같은 hook 명령을 설치한다. `--agent=claude|codex`로 한 도구만 설정할 수 있고, `--dry-run`은 변경 계획만 출력한다. 저장소별 준비 명령은 없다.

- Claude: 각 계정 `settings.json`의 SessionStart에 주입 hook 1개, UserPromptSubmit에 검사 hook 1개.
- Codex: 각 계정 `hooks.json`의 SessionStart에 hook 1개(`additionalContextLimit=0`).

hook 명령의 실행 파일 경로는 `setup`을 실행한 경로 그대로다. 심볼릭 링크로 실행했으면 링크 경로가 저장된다. 다른 경로의 이전 설치본이 남긴 hook은 `status`가 보고하고 `setup`이 교체한다.

`setup`은 hook 설치 후 Claude 계정마다 모델 대화 없는 초기화 명령을 실행한다. 새 계정 설정 환경의 첫 기동 준비이며 저장소마다 실행할 필요는 없다. 초기화 실패는 경고로 안내하고 hook 설치 결과를 바꾸지 않는다. 초기화가 실패했거나 계정 캐시를 지웠으면 로그인·네트워크를 확인하고 `setup --agent=claude`를 다시 실행한다.

**세션 시작 시 주입**

개인 지침은 primary 루트의 `AGENTS.local.md`에 쓰고 Git에서 제외한다. 세션이 시작되면 hook이 hook 입력 `cwd`가 속한 저장소의 primary `AGENTS.local.md`를 읽어 넣는다. linked worktree도 primary의 파일을 읽는다. Git 저장소 밖이거나 파일이 없으면 본문을 넣지 않는다.

- startup·clear·compact에는 전문을 전달한다. resume에는 마지막으로 전달한 내용과 달라졌을 때만 최신 전문을 전달한다.
- SubagentStart와 매 턴 입력에는 붙이지 않는다. 본문은 분할하거나 자르지 않는다.
- 원본은 symlink가 아닌 정규 파일이어야 하고, 유효한 UTF-8이며 NUL이 없고 8MiB 이하여야 한다. 위반이면 본문 대신 `[quota instructions] `로 시작하는 notice를 넣는다.
- Claude는 최종 문자열이 UTF-16 단위 10,000자를 넘으면 전문 대신 짧은 오류를 전달하고 에이전트에게 사용자에게 알리도록 지시한다. UserPromptSubmit 검사도 같은 한도와 원본 오류를 검사해 도구 호출·요청 작업을 보류하도록 지시한다. 이는 에이전트에게 보내는 지시이며 런타임 강제 차단이 아니다. 파일을 고친 뒤에는 새 세션을 시작하거나 resume한다.

세부 계약은 [SPEC의 `agent instructions` 동작](SPEC.md)에 있다.

**지침 파일 배치 권장**

공용 지침은 `AGENTS.md`만 두고, 루트·하위 폴더 모두 `CLAUDE.md`·`.claude/CLAUDE.md`·`CLAUDE.local.md`를 두지 않는다.
Claude Code 기본 설정에서는 세션 시작 디렉터리부터 상위까지 이 파일이 하나라도 있으면(그 계정 설정 디렉터리 안의 전역 `CLAUDE.md`는 제외) 하위 폴더의 `AGENTS.md`를 읽지 않는다. `CLAUDE.md`가 `@AGENTS.md`를 import하는 구성도 같다.
다른 계정의 설정 디렉터리에 있는 `.claude/CLAUDE.md`도 상위 폴더의 프로젝트 파일로 취급된다(예: `~/.claude-2` 계정이 `~/.claude/CLAUDE.md`를 보는 경우).

- 하위 폴더 `AGENTS.md`: Claude Code는 그 폴더의 파일을 Read할 때 함께 불러온다. Read 없이 새 파일을 쓰거나 셸 명령만 실행하면 불러오지 않는다. Codex CLI는 저장소 루트부터 세션 시작 디렉터리까지만 자동으로 읽고, 그 아래는 모델이 직접 찾아 읽으므로 보장되지 않는다.
- `AGENTS.local.md`는 primary 루트에만 둔다. 하위 폴더의 `AGENTS.local.md`는 quota와 두 CLI 모두 읽지 않는다.

하위 폴더 지침 로딩은 CLI native 동작이며 `status`의 검사 범위 밖이다.

**Claude가 만드는 worktree**

Claude Code가 스스로 만드는 worktree에 ignore된 `AGENTS.md`를 복사하려면 원본 checkout에서 `quota-cli agent instructions setup --scope=repo`를 실행한다. `.worktreeinclude`의 기존 항목을 보존하고 필요한 `AGENTS.md` 경로만 추가하며, `--dry-run`으로 먼저 확인할 수 있다.
이 파일은 [Claude의 기본 worktree 생성](https://code.claude.com/docs/en/worktrees#copy-gitignored-files-into-worktrees)과 [Codex 앱의 관리형 worktree](https://learn.chatgpt.com/docs/environments/git-worktrees#copy-ignored-local-files-into-managed-worktrees)가 읽는다. 일반 `git worktree add`와 Codex CLI의 `--worktree`는 처리하지 않으며, 그런 worktree라도 `exec-prompt` 위임은 실행 직전 준비로 지침을 갖춘다.

**설정 확인**

```bash
quota-cli agent instructions status . --agent=all
```

`status`는 모델을 실행하지 않고 다음을 보고한다.

- 계정별 hook 설치 상태: 각 런타임 SessionStart 1개와 Claude UserPromptSubmit 1개의 고정 명령·실행 설정, `disableAllHooks`, Codex trust state.
- 저장소 primary의 `AGENTS.local.md` 존재·원본 검사와 한도 초과.
- `.worktreeinclude` 설정 부재·패턴 제외·ignore 누락과 Claude `WorktreeCreate` 설정.
- 이전 버전 quota가 저장소에 남긴 생성물은 WARN.
- native `AGENTS.md` 읽기를 막는 조건은 WARN: 작업 위치부터 상위의 `CLAUDE.md`·`.claude/CLAUDE.md`·`CLAUDE.local.md`(그 계정 설정 디렉터리의 전역 파일 제외),
  `AGENTS.md`를 읽지 않는 유효 project instruction 모드(`claude-md`·`managed-only`).

설정됨(configured)·차단됨(blocked)·확인 불가(unknown)를 구분하며, 일부만 준비됐거나 확인 불가이면 성공으로 응답하지 않는다.
모델 호출로 실제 전달을 확인하는 검증은 제공하지 않는다.

**해제**

```bash
quota-cli agent instructions uninstall --dry-run
quota-cli agent instructions uninstall
```

계정의 hook을 제거한다. 저장소의 파일은 건드리지 않는다.

#### 스킬 일괄 관리

```bash
quota-cli agent skills install <source> [--skill <name>] [--scope global|repo]
quota-cli agent skills link [<name>]
quota-cli agent skills list
quota-cli agent skills remove <name>
```

스킬은 범위별 `.agents/skills` 아래 스킬 이름별 디렉터리에 두고 Codex는 그 위치를 직접 읽는다. Claude에는 심링크를 만든다. `global`은 홈 디렉터리 기준으로 기본 계정과 등록된 추가 계정 각각에, `repo`는 현재 저장소 루트의 `.claude/skills`에 만든다.
`install`은 소스를 한 번 복사하고 링크하며 기존 경로는 덮어쓰지 않는다. `link`는 빠진 Claude 링크를 만들고, `remove`는 스킬·링크·Codex 중복 위치를 백업 위치로 옮긴다. `list`는 실제 파일시스템을 읽어 연결 상태와 충돌을 보고한다. 소스 취득에는 Node.js와 npm이 필요하다.

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
최소 요구 버전은 Claude Code 2.1.259, Codex CLI 0.153.4이며 미만이거나 필요한 입력 경로·상태 형식을 확인할 수 없으면 원인을 표시하고 건너뛴다.
실행 상태 확인에 `lsof`, Codex 세션 조회에 추가로 `sqlite3`가 필요하다.

"Start at Login"은 다음 로그인부터 적용된다. 현재 실행 중인 앱이나 launchd 작업을 중단하지 않는다.

## 빌드

```bash
go build -o quota-cli ./cmd/quota-cli
go build -o quota-bar ./cmd/quota-bar

# (선택) 재서명하면 System Settings에서 앱 이름이 정상 표시됨
# codesign -s - --force quota-bar
```

quota-bar 메뉴의 버전은 빌드 정보에서 읽는다. 태그로 설치하면 태그 버전, 로컬 빌드는 커밋 해시를 표시하며, `-ldflags "-X main.version=vX.Y.Z"`로 덮어쓸 수 있다.

## 테스트

```bash
go test ./...
```

## 라이선스

MIT
