# 여러 Claude 계정 quota 보기

quota는 Claude 계정 여러 개의 사용량을 동시에 보여줄 수 있다. 이 문서는 **두 번째 Claude
계정을 세팅해서 quota에 표시하기까지**의 전체 순서를 다룬다.

## 원리 (먼저 이해할 것)

- Claude 계정은 **`CLAUDE_CONFIG_DIR`(claude CLI의 설정 디렉터리)로 구분된다.** 디렉터리가
  다르면 서로 다른 계정으로 로그인할 수 있다.
  - 기본 계정: `~/.claude` (환경변수 없을 때)
  - 두 번째 계정: 예) `~/.claude-2`
- quota는 **각 디렉터리에 이미 로그인돼 있는 계정을 읽기만** 한다. 로그인 자체는 claude CLI가
  한다. 즉 순서는 **① claude CLI로 두 번째 계정 로그인 → ② quota에 그 경로 등록**이다.

## 1단계 — 두 번째 계정을 별도 경로에 로그인 (claude CLI)

두 번째 계정용 config 디렉터리를 지정해 로그인한다. `~/.claude-2`는 예시이며 원하는 경로로
바꿔도 된다.

```bash
CLAUDE_CONFIG_DIR="$HOME/.claude-2" claude auth login
```

- 브라우저가 열리면 **첫 번째 계정과 다른 Anthropic 계정**으로 로그인한다.
  (같은 계정으로 로그인하면 quota 값도 첫 계정과 똑같이 나온다 — 의미가 없다.)
- 브라우저에 이미 첫 계정 세션이 남아 있으면 로그아웃하거나 계정 전환 후 진행한다.

로그인이 됐는지 확인:

```bash
CLAUDE_CONFIG_DIR="$HOME/.claude-2" claude auth status
```

> 이미 그 경로에 두 번째 계정이 로그인돼 있다면 이 단계는 건너뛴다.

### 편의: shell alias로 두 번째 계정 상시 사용

매번 `CLAUDE_CONFIG_DIR=...`를 앞에 붙이기 번거로우면 alias를 걸어두면 된다. 두 번째 계정
claude를 `claude2`로 바로 쓸 수 있다. `~/.zshrc`(또는 `~/.bashrc`)에 추가:

```bash
alias claude2='CLAUDE_CONFIG_DIR="$HOME/.claude-2" claude'
```

새 셸을 열거나 `source ~/.zshrc` 후:

```bash
claude2 auth login     # 두 번째 계정 로그인
claude2 auth status    # 로그인 확인
claude2                 # 두 번째 계정으로 Claude Code 실행 (평소 사용)
```

alias는 **claude 실행 편의**일 뿐이다. quota에 계정을 등록할 때(2단계)는 alias가 아니라
**경로**(`~/.claude-2`)로 등록한다.

## 2단계 — quota에 계정 등록

```bash
quota-cli account add claude-2 '~/.claude-2'
```

- `claude-2` = 출력에 쓸 키. **`claude-<숫자>` 형식**이어야 한다(`claude-2`, `claude-3` …).
- `'~/.claude-2'` = 1단계에서 로그인한 경로. 작은따옴표로 감싸면 `~`가 그대로 저장된다(안 감싸도 동작은 동일).
- 경로가 아직 없거나 로그인 안 돼 있으면 경고가 뜨지만 등록은 진행된다.

등록 확인:

```bash
quota-cli account list
```

두 계정이 실제로 다르게 조회되는지 확인:

```bash
quota-cli            # 텍스트
quota-cli --json     # JSON (top-level에 claude, claude-2 가 각각 나옴)
```

`claude`와 `claude-2`의 값이 서로 다르면 성공이다. 한쪽이 `errors`에 나오면 그 계정이
로그인 안 된 것이다(1단계 다시 확인).

## 3단계 — 메뉴바(quota-bar)에 표시

`quota-bar`는 **시작할 때** config를 읽어 계정별 그룹을 만든다. 따라서 계정을 추가/변경한
뒤에는 **재시작해야** 반영된다.

```bash
# 메뉴에서 Quit 후 다시 실행하거나
quota-bar
```

재시작하면 메뉴가 계정별 그룹으로 나뉜다:

```
── Claude ──
   Session / Weekly / …
── Claude 2 ──
   Session / Weekly / …
── Codex ──
   5h / Day
```

각 항목을 체크하면 상단 바에 남은 %가 표시된다.

## 계정 더 추가 / 제거

셋 이상도 같은 방식이다 — 각 계정을 별도 경로에 로그인한 뒤 `account add`로 등록한다.

```bash
quota-cli account add claude-3 '~/.claude-3'   # 추가
quota-cli account rm  claude-2                 # 제거
```

추가/제거 후 quota-bar는 재시작해야 메뉴에 반영된다. (등록 내용은 `~/.config/quota/config.json`에 저장된다.)

## 트러블슈팅

| 증상 | 원인 / 해결 |
|------|-------------|
| `claude-2`가 `errors`에 나옴 | 그 경로에 로그인 안 됨 → `CLAUDE_CONFIG_DIR=... claude auth login` |
| 두 계정 값이 똑같음 | 두 경로가 같은 계정으로 로그인됨 → 두 번째를 다른 계정으로 다시 로그인 |
| `account add`가 "형식" 오류 | 키가 `claude-<숫자>` 형식이 아님 (예: `claude2`, `work`는 안 됨) |
| quota-bar에 안 나옴 | config 변경 후 재시작 안 함 → quota-bar 재시작 |
