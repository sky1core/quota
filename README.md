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
- **기본 계정은 실행 환경의 `CLAUDE_CONFIG_DIR`/`CODEX_HOME`을 그대로 따른다.** 그 변수가 설정된 셸(예: 에이전트 CLI 안)에서 `quota-cli`를 돌리면 기본 계정 행이 그 계정을 조회하므로, 같은 실제 디렉터리를 추가 계정으로도 등록해 두었다면 충돌한 계정들은 조회 대상에서 제외하고 오류를 보고한다. 심볼릭 링크로 같은 위치를 가리키는 경우도 중복으로 판단한다. 기본 계정을 고정해서 보려면 변수를 지우고 실행한다(`env -u CLAUDE_CONFIG_DIR quota-cli`).

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
기본 계정의 모델 조회는 CLI 환경을 상속한다. `CLAUDE_CONFIG_DIR`·`CODEX_HOME`을 직접 설정한다면 절대경로를 사용해야 하며, 상대경로이면 모델 조회를 오류로 중단한다.
모델 캐시는 계정 경로와 해당 환경변수의 지정 여부·값을 함께 구분한다.
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

기본 preset `github-history-guard`는 PR/Issue 생성·수정 같은 GitHub 협업 메타데이터 작업은 허용하고,
`git push`, `git send-pack`, `git pull`, `git merge`, `git rebase`, `git commit --amend`, `git reset --hard`,
`git filter-branch`, `git hook run`, `git for-each-repo`, `git update-ref`, `git replace`, `git reflog expire`, 강제 branch reset, branch
delete/move/copy, tag force/delete, `gh pr merge`, `gh pr update-branch`, `gh pr checkout/co --force`, `gh repo sync`, `gh release create`,
`gh release delete`, raw `gh api`처럼 코드 이력이나 ref 상태를 바꾸는 명령은 차단한다. 보호 명령을 숨길 수 있는
`git config alias.*`/`include.*`, shell `alias`/`source`/`.`/`trap`/`xargs`,
`gh alias set/import/delete`, `gh extension exec`, 알 수 없는 `git`/`gh` alias·extension dispatch도 차단한다.
`gh stack link <number> <number>`만 허용하고 다른 `gh stack ...` 형태는 차단한다.

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

`agent instructions`는 `AGENTS.md`와 `AGENTS.local.md`를 Claude Code와 Codex CLI에서 함께 사용하도록 준비한다.
실행부는 quota-cli에 포함되며 별도 스크립트·Python·hook spec이 필요하지 않다.
Git 2.36 이상, Claude Code 2.1.232 이상·Codex CLI 0.154.0 이상을 대상으로 한다.

**관리할 파일과 처음 설정**

공용 지침은 각 checkout의 `AGENTS.md`, 개인 지침은 primary의 `AGENTS.local.md`에 작성한다. 개인 파일은 Git에서 제외한다.
여기서 primary는 Git 저장소의 기본 작업 폴더이며, bare 저장소에서는 bare 루트다.
Codex용 `AGENTS.override.md`와 linked worktree의 로컬 복사본은 quota가 생성·갱신한다. 사용자가 직접 만들거나 편집할 파일이 아니다.
기존 사용자 override나 사용자가 수정한 생성물은 덮어쓰지 않고 충돌을 보고한다.

아래는 두 도구를 함께 준비하는 예다. 한 도구만 쓰면 각 명령의 `--agent=all`을 `--agent=claude` 또는 `--agent=codex`로 바꾼다.

```bash
quota-cli agent instructions setup . --agent=all --dry-run
quota-cli agent instructions setup . --agent=all
quota-cli agent instructions status . --agent=all
```

`--dry-run`은 같은 사전 검사와 변경 계획을 보여주며 원본·관리 파일·계정 설정을 쓰지 않는다. CLI 시작 과정의 런타임 캐시는 갱신될 수 있다.
`all`은 현재 환경으로 선택되는 공급자별 계정 하나씩이며 등록 계정 전체가 아니다.
`setup`은 현재 Claude 계정의 hook 연결을 설치하고, Codex 계정의 정확히 식별된 이전 지침 주입 hook은 제거한다.
무관한 사용자 hook은 보존한다. 계정의 hook 제거는 그 계정을 사용하는 다른 저장소에도 적용되므로 해당 저장소도 native 로딩으로 준비해야 한다.

**평소 사용과 원본 변경**

Claude는 `CLAUDE.md`·`CLAUDE.local.md`의 native 로딩을 사용한다.
Codex는 개인 원본이 있으면 공용·개인 본문을 합친 관리 `AGENTS.override.md`를, 없으면 공용 `AGENTS.md`를 native로 읽는다.
준비한 작업 폴더에서는 평소처럼 CLI를 직접 실행하거나 기존 세션을 `resume`하면 된다. 같은 본문을 hook이나 매 턴 입력에 다시 붙이지 않는다.

| 상황 | 할 일 |
|---|---|
| 원본·관리 파일과 계정·설정이 그대로임 | 준비된 폴더에서 그대로 실행·재개한다. 매번 `setup`할 필요는 없다. |
| 원본 지침 또는 등록한 로컬 파일을 변경함 | 다음 실행·재개 전에 `setup`을 다시 실행해 복사본·병합본을 갱신한다. |
| 사용할 계정이나 Codex 설정을 바꿈 | 해당 계정·환경에서 `status`로 확인하고, 준비가 필요하면 `setup`한다. |
| 외부 `git worktree add`로 새 checkout을 만듦 | 해당 checkout에서 agent 실행 전에 `setup`한다. |
| Claude의 관리된 WorktreeCreate로 만듦 | quota가 파일 준비를 마친 뒤 새 worktree 경로를 반환한다. |

실행 중 원본 변경을 자동으로 감시·반영하지 않는다. 지침 변경 뒤 기존 세션을 재개하면 이전 본문이 컨텍스트에 남을 수 있다.
quota는 과거 지침을 선택 삭제하거나 자동 압축하지 않는다.

공용 파일은 각 checkout의 버전이 기본이다. primary의 미추적 공용 파일도 복사하려면 `setup --shared-source=primary`를 지정한다.
추가 ignored 파일은 `setup --local-file=config/example.local.json`처럼 primary 기준 상대 경로로 등록한다. 여러 파일은 옵션을 반복한다.
복사 대상은 8MiB 이하의 ignored·untracked 정규 파일이며 소유자 실행 권한을 보존한다. `.gitignore` 자체는 등록할 수 없다.
등록은 다음 setup에도 유지된다. 목록을 비우려면 아래 repository 해제를 두 공급자 모두에 적용한 뒤 필요한 목록으로 다시 setup한다. 원본 파일은 보존한다.

**설정 확인과 중단 조건**

`status`는 모델 호출 없이 관리 파일의 소유권·최신성과 Codex의 유효 설정을 검사한다. 준비되지 않았거나 확인할 수 없으면 성공으로 처리하지 않는다.
`setup`은 모든 선택 대상의 사전 검사를 쓰기 전에 수행하며, 다음 경우 파일을 적용하지 않고 중단한다.

- 사용자 파일과 충돌하거나 Codex의 신뢰·문서 탐색 설정·지침 크기 제한이 전달 조건을 충족하지 못한 경우.
- Codex가 실제로 읽는 설정·hook의 위치를 적용 예정 파일과 대응시킬 수 없는 경우. 예를 들어 로컬 파일 복사로 없던 `.codex` 폴더를 만들면, 생성 후 읽힐 hook의 출처를 사전에 확인할 수 없어 중단할 수 있다.

오류에 표시된 경로와 원인을 확인하고, 의도한 설정이 Codex에서 확인 가능한 상태가 된 뒤 `setup --dry-run`부터 다시 실행한다.
quota가 출처 불명 파일을 자동으로 채택하거나 신뢰 설정을 임의로 바꾸지는 않는다.
`setup` 실행 중에는 외부 편집기·프로그램으로 Codex 전역 설정이나 관리자 정책을 바꾸지 않는다. quota의 잠금은 이런 외부 변경과의 동시 적용까지 보장하지 않는다.
사전 검사 통과 후 파일 쓰기 중 오류가 나면 일부 적용됐을 수 있다. 출력의 적용·실패 경로를 확인하고 원인을 해결한 뒤 setup과 status를 다시 실행한다.

**실제 전달 검증**

```bash
quota-cli agent instructions verify . --agent=codex
```

`verify`는 같은 전제 검사 후 임시 저장소의 새 메인 세션에서 전달·비전달 대조군을 실행하므로 쿼터를 사용한다.
확인 불가·도구 사용·불완전한 응답은 검증 성공이 아니다. 이 명령의 성공을 서브에이전트·resume·compact의 전달 보장으로 확대하지 않는다.
Claude의 native memory를 생략하는 내장 subagent와 호출별 설정 override는 이 검증 범위 밖이다.

**해제**

```bash
quota-cli agent instructions uninstall . --scope=repository --agent=all
quota-cli agent instructions uninstall --scope=account --agent=codex
```

repository 해제는 같은 Git common-dir의 선택 공급자 지침을 비활성화하고 관리 생성물을 정리한다.
account 해제는 해당 CLI 설정 홈의 연결을 제거하므로 그 계정의 다른 저장소에도 영향을 준다.
원본 파일은 보존한다. 다시 `setup`하면 활성화된다. Codex native 공용 지침 로딩을 끄는 명령은 아니다.

#### Agent overlay — 기존 spec 호환 명령

사용자 정의 외부 명령을 연결하는 기존 spec도 지원한다. 아래 스키마의 구체 명령은 로컬 spec에 있다.
설치·진단 명령은 기본적으로 `~/.config/quota/agent-overlay.json`을 읽으며
`--spec <file>`로 재정의한다. 이 spec이 없으면 `plan`·`apply`·`doctor`·`verify`는 명시적으로
실패한다(암시적 기본값 없음). `agent instructions`는 이 spec을 사용하지 않는다. spec 스키마는 `version`(1 고정), 선택적 `claude`/`codex`/`verify`
섹션이며, hook 이벤트 이름은 고정 목록이 아니라 spec에 적힌 키를 그대로 쓴다.

```json
{
  "version": 1,
  "claude": {
    "hooks": { "SessionStart": [ { "command": "/path/to/overlay-hook session" } ] },
    "replaces": [ "/path/to/overlay-hook session --previous" ]
  },
  "codex": {
    "settings": { "project_doc_max_bytes": 32768 },
    "hooks": { "SessionStart": [ { "command": "/path/to/overlay-hook codex-session", "additionalContextLimit": 0 } ] }
  },
  "verify": {
    "claude": { "command": ["/path/to/overlay-hook", "verify", "claude"] },
    "codex": { "command": ["/path/to/overlay-hook", "verify", "codex"] }
  }
}
```

spec JSON은 알 수 없는 필드를 허용하지 않아 오타 키는 조용히 무시하지 않고 load를 실패시킨다.
`claude.replaces`는 이전 버전이 설치했던 command 문자열 배열로, 비교는 문자열 완전 일치(`==`)뿐이며
접두·패턴·argv[0] 해석은 없다.

- `init [--force]`: placeholder 명령이 든 spec 템플릿 생성. 기존 파일은 `--force` 없이 거부.
- `plan [--runtime=all|claude|codex]`: 대상 파일 경로와 이벤트별 상태(Claude present/missing/stale,
  Codex present/missing/mismatch)를 보여주며 파일을 수정하지 않는다.
- `apply [--runtime=...]`: Claude는 `CLAUDE_CONFIG_DIR/settings.json`(없으면 `~/.claude/settings.json`)의
  `hooks.<event>`에 spec 엔트리를 충돌 없는 백업 후 원자적으로 설치한다. 같은 이벤트에서 command
  문자열이 spec command와 완전 일치하거나 `claude.replaces`에 든 command와 완전 일치하는 managed
  엔트리만 spec 버전으로 교체하고, 그 외 엔트리는 보존한다(argv[0] 추론 없음). **Codex apply는 지원하지
  않는다**(`config.toml`은 주석·trust hash가 있는 대형 TOML이라 자동 재작성이 위험) — Codex는
  `plan`/`doctor`로 확인하고 손으로 반영한다. 새 hook은 Codex의 `/hooks`에서 검토·신뢰해야
  실행된다([hook 신뢰 절차](https://developers.openai.com/codex/hooks#review-and-trust-hooks)).
  quota의 `doctor`는 Codex에 저장된 hook 신뢰 상태를 검사하지 않는다.
- `doctor [--runtime=...]`: verify 명령을 실행하지 않으며 최고 상태는 `installed`다. runtime별 상태를
  `installed`(검사한 대상 파일의 관리 항목이 지원 형태와 일치하고 검사 대상인 명시적 방해 조건이 없음),
  `degraded`(누락/불일치 또는 실효 저해 요인 — 원인, 누락 엔트리, Codex는 추가할 TOML 스니펫과 값
  불일치 replace 안내 출력), `unconfigured`(spec에 해당 runtime 없음), `error`(대상 파일 파싱 불가)로
  보고한다. Claude settings 루트의 `disableAllHooks: true`는 엔트리가 있어도 `degraded`다. Codex는
  `CODEX_HOME/config.toml`(없으면 `~/.codex/config.toml`)을 **읽기 전용**으로 파싱하며, hook 엔트리는
  `type=="command"`일 때만 존재로 인정한다. 누락 키/hook은 add 스니펫으로, 값 불일치 키는
  교체용 별도 안내로 출력한다. Codex 기능 키는 `features.hooks`가 구형 별칭보다 우선하며, 지정된 키는 모두 boolean이어야 한다.
  관리자 전용 키를 사용자 파일에 둔 것은 비활성화로 판정하지 않으며 관리자 정책 자체는 검사하지 않는다. degraded/error가 있으면 exit 1.
- `verify [--spec <file>]`: doctor 엔트리 검사에 더해 엔트리가 설치된 configured 런타임의
  `verify.<runtime>.command`를 현재 작업 디렉터리에서 실행한다. exit 0이면 그 런타임을 `enforced`로
  올리고, 명령이 없으면 "live verification not configured", 명령이 실패하면 exit code를 원인으로 한
  `degraded`다. 모든 configured 런타임이 `enforced`일 때만 exit 0이다.

설치·스니펫 생성·진단은 같은 기대 설정을 사용한다. command가 같아도 그룹 조건이나 실행 필드가
다르면 정상으로 인정하지 않는다. 빈 matcher와 생략, 문자열 statusMessage만 동등한 표현으로
허용하며, `if`·`args` 등 지원하지 않는 추가 필드는 원인과 함께 `degraded`로 보고한다.
Codex의 `features.hooks = false`도 진단하며, apply는 전역 비활성화 설정을 임의로 변경하지 않는다.
`installed`는 실제 실행 성공을 뜻하지 않고, `enforced`는 지정된 검증 명령이 확인한 범위만 보장한다.

Claude overlay와 agent hooks의 JSON 설치는 경로별 잠금 안에서 최신 설정을 읽고 수정·백업·저장한 뒤
다시 읽어 확인한다. 서로 다른 quota 프로세스의 동시 적용을 직렬화하며 외부 편집기는 이 보장 대상이
아니다. 신규 파일 권한에는 umask를 적용하고 기존 권한은 보존한다. 반복 적용으로 값이 같으면
파일이나 백업을 쓰지 않는다. JSON 숫자의 정밀도를 보존하며,
Claude의 Codex 전용 필드, 음수 context limit, TOML 숫자 범위 초과, `replaces`와 현재 명령의 중복은
입력 오류로 거부한다.

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
