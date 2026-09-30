# PRD: Shed (가칭) — 개발자를 위한 안전한 디스크 확보 도구

| 항목 | 내용 |
|---|---|
| 문서 상태 | Draft v0.1 |
| 작성일 | 2026-09-30 |
| 제품명 | Shed (가칭, 상표/패키지명 충돌 확인 필요) |
| 형태 | CLI 우선 → Wails 기반 데스크톱 GUI |
| 언어 | Go (코어/CLI/GUI 백엔드 공유) |
| 태그라인 | Reclaim gigabytes. Lose nothing you can't rebuild. |

---

## 1. 배경과 문제 정의

개발자 머신의 디스크는 "내가 만든 것"보다 "도구가 만든 것"으로 채워진다. node_modules, Rust `target`, Xcode DerivedData, Docker 이미지, 패키지 매니저 캐시, 그리고 최근에는 수 GB~수십 GB 단위의 로컬 AI 모델(Ollama, Hugging Face, LM Studio)까지 쌓인다.

기존 도구들은 각자 일부만 해결한다.

| 도구 | 잘하는 것 | 한계 |
|---|---|---|
| kondo | 마커 파일로 프로젝트 타입을 인식해 산출물만 정확히 찾음 | 프로젝트 폴더 안만 봄. 전역 캐시 미지원. 영구 삭제(`rm -rf`). GUI 정체 |
| npkill | `npx` 한 줄로 즉시 실행, node_modules 탐색 UX | 사실상 Node 전용. 이름 매칭 기반. 영구 삭제 |
| ncdu | 시스템 전체를 크기순으로 탐색 | "지워도 되는지" 판단을 전혀 해주지 않음 |
| OS 기본 저장공간 관리 | 일반 사용자용 정리 | 개발 도구 캐시를 이해하지 못함 |

결과적으로 개발자는 다음과 같은 문제를 겪는다.

1. **어디가 큰지 모른다.** 용량의 대부분이 프로젝트 밖 전역 캐시에 있는데 이를 한눈에 보는 수단이 없다.
2. **지워도 되는지 모른다.** `~/Library/Developer`의 40GB가 안전한 캐시인지 중요한 아카이브인지 판단하기 어렵다.
3. **어떻게 지워야 하는지 모른다.** `rm -rf`로 지우면 안 되고 `docker image prune`, `brew cleanup`, `go clean -modcache`처럼 도구별 정석 명령이 있는 경우가 많다.
4. **지운 뒤가 불안하다.** 모든 기존 도구가 영구 삭제라 실수하면 복구할 수 없다.

## 2. 제품 비전

> 시스템 전체를 스캔해 개발 관련 데이터를 **안전 등급별로 분류**하고, **왜 안전한지·어떻게 재생성되는지·어떻게 지우는 게 정석인지**를 설명한 뒤, **복구 가능한 방식으로** 디스크를 확보해주는 도구.

핵심 약속은 하나다. **"다시 만들 수 있는 것만 지운다. 그리고 지운 것도 한동안은 되돌릴 수 있다."**

## 3. 목표와 비목표

### 3.1 목표

- **G1. 전체 시야**: 프로젝트 산출물 + 전역 캐시 + AI 모델 + 컨테이너/VM 이미지를 한 번의 스캔으로 파악한다.
- **G2. 신뢰할 수 있는 분류**: 모든 항목에 안전 등급, 근거, 재생성 방법, 재생성 비용을 붙인다.
- **G3. 정석대로 삭제**: 도구가 공식 정리 명령을 제공하면 그것을 우선 사용한다(Native-first).
- **G4. 복구 가능성**: 기본 삭제는 격리(Quarantine) 후 유예 기간을 두고 영구 삭제한다.
- **G5. CLI와 GUI의 동등성**: 모든 기능은 CLI에서 먼저 제공되고, GUI는 같은 코어를 사용한다.

### 3.2 비목표

- 일반 사용자용 시스템 최적화 도구(CleanMyMac류의 메모리 정리, 앱 제거, 악성코드 검사 등)는 다루지 않는다.
- 사용자 문서, 사진, 미디어 파일의 중복 제거나 정리는 v1 범위 밖이다.
- 루트 권한이 필요한 시스템 영역(`/System`, `C:\Windows`) 정리는 하지 않는다.
- 클라우드 스토리지 정리는 다루지 않는다.

## 4. 타깃 사용자

| 페르소나 | 상황 | 핵심 니즈 |
|---|---|---|
| 풀스택/프론트엔드 개발자 | 사이드 프로젝트 수십 개, 각각 node_modules 수백 MB | 안 쓰는 프로젝트만 골라서 한 번에 정리 |
| 모바일 개발자 | Xcode, 시뮬레이터, Android 에뮬레이터로 100GB+ 사용 | Xcode 관련 폴더의 안전성 판단 |
| AI/ML 개발자 | Ollama·HF 모델, conda 환경, 데이터셋 캐시 | 어떤 모델이 오래 안 쓰였는지, 재다운로드 비용 |
| 폴리글랏/인프라 개발자 | Go, Rust, Java, Docker를 모두 사용 | 흩어진 캐시를 한 곳에서 관리 |
| 256GB/512GB 노트북 사용자 | 늘 디스크 부족 경고 | 빠르고 안전하게 수십 GB 확보 |

## 5. 핵심 개념

### 5.1 안전 등급 (Safety Tier)

모든 발견 항목은 네 등급 중 하나로 분류된다.

| 등급 | 의미 | 예시 | 기본 동작 |
|---|---|---|---|
| 🟢 **Safe** | 명령 한 번으로 완전히 재생성 가능하고 사용자 고유 데이터가 없음 | lockfile이 있는 node_modules, `target/`, `__pycache__`, npm/pip/go-build 캐시, DerivedData | 일괄 선택 가능 |
| 🟡 **Caution** | 재생성은 가능하지만 시간·대역폭 비용이 크거나 환경 재현이 어긋날 수 있음 | Ollama/HF 모델, Docker 이미지, 시뮬레이터 런타임, lockfile 없는 node_modules, `.venv` | 개별 확인 후 선택 |
| 🟠 **Review** | 재생성 불가능할 수 있어 사용자 판단 필요 | Xcode Archives(dSYM 포함), 오래된 VM 이미지, 대용량 로그, Docker 볼륨 | 기본 미선택, 경고와 함께 표시 |
| 🔴 **Protected** | 절대 삭제 대상이 아님 | git이 추적하는 파일, 소스 코드, `.git`, SSH 키, 설정 파일, 키체인 | 표시만 하고 선택 불가 |

### 5.2 재생성 비용 (Rebuild Cost)

"지워도 된다"만으로는 부족하다. 다시 만드는 데 드는 비용을 함께 보여준다.

- **재생성 방법**: 예) `npm ci`, `cargo build`, `ollama pull llama3`
- **예상 비용**: 없음 / 낮음(수 초~수 분) / 중간(재다운로드 수 GB) / 높음(수십 GB, 오프라인 불가)
- **재현성**: lockfile 존재 여부, 버전 고정 여부

### 5.3 안전 신호 (Safety Signals)

등급은 규칙만으로 정하지 않고, 실제 상태 신호로 보정한다.

| 신호 | 효과 |
|---|---|
| git이 해당 경로를 ignore 처리함 (`git check-ignore`) | 산출물일 가능성 ↑ (등급 유지 또는 상향) |
| git이 해당 경로 내 파일을 추적함 | 즉시 Protected로 격상 |
| lockfile 존재 (`package-lock.json`, `Cargo.lock`, `go.sum` 등) | node_modules 등이 Safe로 확정 |
| lockfile 부재 | Caution으로 하향 |
| 최근 수정/접근 시각 | "활성 프로젝트" 표시, 기본 선택 해제 |
| 실행 중인 프로세스가 해당 경로 사용 중 | 삭제 차단 |
| 앱 번들 내부 (`*.app/`, Electron 앱 리소스) | Protected (npkill의 ⚠ 경고와 동일한 문제 방지) |

### 5.4 Native-first 삭제

도구가 공식 정리 명령을 제공하면 파일 직접 삭제보다 그 명령을 우선한다. 도구의 내부 인덱스·DB와 불일치를 막기 위함이다.

| 대상 | 정석 명령 |
|---|---|
| Docker | `docker image prune`, `docker builder prune`, `docker system df`로 사전 확인 |
| Homebrew | `brew cleanup --prune=all` |
| Go | `go clean -cache`, `go clean -modcache` |
| npm | `npm cache clean --force` |
| pnpm | `pnpm store prune` |
| yarn | `yarn cache clean` |
| pip | `pip cache purge` |
| conda | `conda clean --all` |
| Ollama | `ollama rm <model>` |
| Hugging Face | `hf cache` / `huggingface-cli delete-cache` |
| iOS 시뮬레이터 | `xcrun simctl delete unavailable` |
| Gradle | 데몬 중지(`--stop`) 후 캐시 디렉터리 삭제 |

Native 명령은 격리가 불가능하므로 **되돌릴 수 없음** 배지를 붙이고 재생성 방법을 더 강조해서 보여준다.

### 5.5 격리 (Quarantine)

- 기본 삭제는 영구 삭제가 아니라 **격리 폴더로 이동**이다.
- 같은 볼륨 안에서는 `rename`으로 즉시 이동되므로 대용량도 빠르다. 단, 이 단계에서는 공간이 확보되지 않는다는 점을 UI에 명확히 표시한다.
- 유예 기간(기본 7일)이 지나면 영구 삭제되고, 사용자는 언제든 `purge`로 즉시 비울 수 있다.
- 다른 볼륨에 걸친 항목은 복사 비용이 크므로 격리 대신 OS 휴지통(macOS Trash, Windows 휴지통, freedesktop Trash) 또는 명시적 영구 삭제 중 선택하게 한다.
- 모든 작업은 매니페스트(원래 경로, 크기, 시각, 규칙 ID)로 기록되어 복원에 사용된다.

## 6. 탐지 대상 (초기 규칙 카탈로그)

### 6.1 프로젝트 산출물 (마커 기반)

| 생태계 | 마커 | 대상 | 기본 등급 |
|---|---|---|---|
| Node | `package.json` | `node_modules`, `.next`, `.nuxt`, `.turbo`, `.parcel-cache` | Safe (lockfile 없으면 Caution) |
| Rust | `Cargo.toml` | `target/` | Safe |
| Go | `go.mod` | (프로젝트 내 산출물 거의 없음, 바이너리 결과물만) | Review |
| Python | `pyproject.toml`, `requirements.txt` | `__pycache__`, `.pytest_cache`, `.mypy_cache`, `.ruff_cache` | Safe |
| Python | 동일 | `.venv`, `venv` | Caution |
| Java/Kotlin | `build.gradle(.kts)`, `pom.xml` | `build/`, `.gradle/`, `target/` | Safe |
| Swift | `Package.swift` | `.build/` | Safe |
| .NET | `*.csproj`, `*.sln` | `bin/`, `obj/` | Safe |
| Unity | `ProjectSettings/` | `Library/`, `Temp/`, `Obj/` | Safe |
| Unreal | `*.uproject` | `Intermediate/`, `DerivedDataCache/`, `Binaries/` | Safe |
| Dart/Flutter | `pubspec.yaml` | `.dart_tool/`, `build/` | Safe |
| Zig | `build.zig` | `zig-cache/`, `.zig-cache/`, `zig-out/` | Safe |

> `build/`, `dist/`처럼 흔한 이름은 반드시 마커 파일과 함께 있을 때만, 그리고 git ignore 상태일 때만 Safe로 판정한다.

### 6.2 전역 캐시 (경로 기반, OS별)

| 대상 | macOS | Linux | Windows | 기본 등급 |
|---|---|---|---|---|
| npm cache | `~/.npm/_cacache` | `~/.npm/_cacache` | `%LocalAppData%\npm-cache` | Safe |
| pnpm store | `~/Library/pnpm/store` | `~/.local/share/pnpm/store` | `%LocalAppData%\pnpm\store` | Safe |
| pip cache | `~/Library/Caches/pip` | `~/.cache/pip` | `%LocalAppData%\pip\Cache` | Safe |
| Go build cache | `~/Library/Caches/go-build` | `~/.cache/go-build` | `%LocalAppData%\go-build` | Safe |
| Go module cache | `~/go/pkg/mod` | `~/go/pkg/mod` | `%UserProfile%\go\pkg\mod` | Safe |
| Cargo registry | `~/.cargo/registry`, `~/.cargo/git` | 동일 | 동일 | Safe |
| Maven | `~/.m2/repository` | 동일 | 동일 | Safe |
| Gradle | `~/.gradle/caches`, `~/.gradle/wrapper/dists` | 동일 | 동일 | Safe |
| Homebrew | `brew --cache` | (Linuxbrew 동일) | — | Safe |
| Xcode DerivedData | `~/Library/Developer/Xcode/DerivedData` | — | — | Safe |
| iOS DeviceSupport | `~/Library/Developer/Xcode/iOS DeviceSupport` | — | — | Caution (기기 재연결 시 재생성) |
| iOS 시뮬레이터 | `~/Library/Developer/CoreSimulator` | — | — | Caution |
| Xcode Archives | `~/Library/Developer/Xcode/Archives` | — | — | Review (dSYM 포함) |
| Android AVD | `~/.android/avd` | 동일 | `%UserProfile%\.android\avd` | Caution |
| Docker | Docker Desktop VM 디스크 | `/var/lib/docker` (권한 필요) | Docker Desktop | Caution (Native 명령만 사용) |
| Hugging Face | `~/.cache/huggingface/hub` | 동일 | `%UserProfile%\.cache\huggingface` | Caution |
| Ollama | `~/.ollama/models` | `~/.ollama/models` | `%UserProfile%\.ollama\models` | Caution |
| LM Studio | `~/.lmstudio/models` | 동일 | 동일 | Caution |
| conda pkgs | `~/miniconda3/pkgs` 등 | 동일 | 동일 | Safe |
| JetBrains 캐시 | `~/Library/Caches/JetBrains` | `~/.cache/JetBrains` | `%LocalAppData%\JetBrains` | Safe (IDE 종료 필요) |

> 경로는 환경 변수(`GOMODCACHE`, `CARGO_HOME`, `HF_HOME`, `OLLAMA_MODELS` 등)로 재정의될 수 있으므로, 규칙은 환경 변수와 도구의 설정 조회 명령(`go env GOMODCACHE`, `npm config get cache` 등)을 우선 확인한다.

## 7. 기능 요구사항

### 7.1 스캔 (Scan)

- **FR-1** 사용자 홈 디렉터리와 지정한 경로를 병렬로 스캔한다.
- **FR-2** 전역 캐시 규칙은 전체 트리 순회 없이 알려진 경로를 직접 조회해 빠르게 결과를 낸다.
- **FR-3** 프로젝트 탐색은 마커 파일을 찾으면 해당 프로젝트 하위의 산출물 폴더만 측정하고 더 깊이 내려가지 않는다(성능).
- **FR-4** 기본적으로 심볼릭 링크를 따라가지 않고, 파일시스템 경계를 넘지 않는다.
- **FR-5** 하드링크(pnpm store 등)는 실제 점유 용량 기준으로 중복 없이 계산한다.
- **FR-6** 스캔 결과는 캐시하여 재실행 시 변경분만 다시 측정한다.
- **FR-7** 스캔 중에도 결과를 점진적으로 스트리밍한다(CLI 진행률, GUI 실시간 갱신).

### 7.2 분류와 설명 (Classify & Explain)

- **FR-8** 모든 항목에 등급, 규칙 ID, 크기, 마지막 수정/접근 시각, 소속 프로젝트를 붙인다.
- **FR-9** 항목마다 "왜 이 등급인가"를 사람이 읽을 수 있는 문장으로 제공한다.
  - 예: "이 폴더는 `package-lock.json`이 있는 프로젝트의 의존성입니다. git이 무시하는 경로이며 `npm ci`로 동일하게 복원됩니다."
- **FR-10** 재생성 방법(명령어)과 재생성 비용을 함께 보여준다.
- **FR-11** 안전 신호(5.3)에 따라 등급을 자동 보정하고, 보정 이유를 기록한다.

### 7.3 가이드 (Guide)

- **FR-12** 각 규칙은 "정석 삭제 방법" 가이드를 포함한다: Native 명령, 사전 조건(예: IDE 종료, Docker 실행 중), 주의사항.
- **FR-13** 사용자가 Shed로 직접 지우지 않고 가이드만 보고 수동으로 처리할 수 있도록 명령어 복사 기능을 제공한다.
- **FR-14** 우선순위 추천: "가장 적은 위험으로 가장 많이 확보하는 순서"로 정렬된 정리 플랜을 제안한다.

### 7.4 정리 (Clean)

- **FR-15** 정리 전 반드시 요약(항목 수, 총 용량, 등급별 분포, 되돌릴 수 없는 항목)을 보여주고 확인을 받는다.
- **FR-16** `--dry-run`은 실제 동작 없이 수행될 작업 목록만 출력한다.
- **FR-17** 기본 삭제 방식은 격리. Native 명령 대상은 되돌릴 수 없음을 명시한다.
- **FR-18** 삭제 직전 안전 신호를 재검사한다(스캔과 정리 사이의 상태 변화 대응: git 추적 여부, 사용 중 프로세스).
- **FR-19** Protected 항목은 어떤 옵션으로도 삭제할 수 없다.
- **FR-20** 필터: 등급, 생태계, 크기 하한, 마지막 수정 기간(`--older 90d`), 경로 포함/제외.

### 7.5 복원과 이력 (Restore & History)

- **FR-21** 격리된 항목을 원래 경로로 복원한다. 원래 경로에 이미 무언가 있으면 충돌을 알리고 중단한다.
- **FR-22** 정리 이력(언제, 무엇을, 얼마나)을 보관하고 누적 확보 용량을 보여준다.
- **FR-23** 격리 보관 기간 설정, 즉시 비우기(`purge`)를 지원한다.

### 7.6 설정 (Config)

- **FR-24** 사용자 설정 파일(`~/.config/shed/config.yaml`)에서 스캔 루트, 제외 경로, 격리 기간, 규칙 활성화 여부를 지정한다.
- **FR-25** 사용자 정의 규칙을 추가하거나 기본 규칙의 등급을 재정의할 수 있다. 단, Protected 해제는 불가.
- **FR-26** 프로젝트 단위 제외 파일(`.shedignore`)을 지원한다.

## 8. CLI 설계

### 8.1 명령 구조

```
shed scan [paths...]            # 스캔 후 요약 출력
shed report [--tier safe|caution|review] [--json]
shed explain <path|rule-id>     # 등급 근거, 재생성 방법, 정석 삭제 가이드
shed plan                       # 추천 정리 플랜 (위험 대비 확보 용량 순)
shed clean [filters] [--dry-run] [--yes] [--permanent]
shed restore <id|--last>
shed quarantine list|purge
shed history
shed rules list|show <rule-id>
shed doctor                     # 권한, 도구 설치 여부, 환경 변수 점검
```

### 8.2 출력 예시

```
$ shed scan ~
Scanning... 1,284 projects, 23 global caches (12.4s)

  Reclaimable      Tier       Items
  ───────────────────────────────────
  48.2 GB          🟢 Safe      312
  71.9 GB          🟡 Caution    27
  18.3 GB          🟠 Review      6
  ───────────────────────────────────
  (Protected items are hidden. Use --show-protected)

Top opportunities
  🟢 23.1 GB  node_modules × 214   (projects untouched > 90 days: 19.8 GB)
  🟢 11.4 GB  Xcode DerivedData
  🟡 38.6 GB  Ollama models × 6    (3 unused > 60 days: 21.2 GB)
  🟡 19.0 GB  Docker images        (dangling: 6.3 GB)

Next: `shed plan` for a step-by-step cleanup plan.
```

```
$ shed explain ~/code/old-app/node_modules
🟢 Safe · rule: node.modules · 612 MB · last modified 214 days ago

Why safe
  - package-lock.json exists → exact versions are reproducible
  - Path is ignored by git (.gitignore: node_modules/)
  - No running process is using this directory

How to rebuild
  cd ~/code/old-app && npm ci        (cost: low, ~1 min, ~600 MB download)

How Shed removes it
  Moves to quarantine (restorable for 7 days)
```

### 8.3 CLI 원칙

- 파괴적 명령은 항상 확인을 받는다. 스크립트용으로 `--yes` 제공.
- `--json` 출력을 모든 조회 명령에 제공한다(GUI, 스크립트, 다른 도구 연동용).
- 종료 코드: 0 성공, 1 오류, 2 사용자 취소, 3 안전 검사로 차단된 항목 존재.
- sudo 없이 동작하는 것을 기본으로 한다.

## 9. GUI 설계 (Wails)

### 9.1 화면 구성

1. **대시보드**
   - 디스크 전체 용량 대비 확보 가능 용량 게이지 (등급별 색상)
   - 카테고리별 트리맵 (프로젝트 산출물 / 패키지 캐시 / IDE·빌드 캐시 / AI 모델 / 컨테이너·에뮬레이터)
   - "추천 플랜" 카드: 원클릭으로 Safe 항목 정리
2. **탐색 화면**
   - 좌측: 카테고리·생태계 트리, 필터(등급, 기간, 크기)
   - 중앙: 항목 목록(크기순, 마지막 사용일, 등급 배지)
   - 우측: 상세 패널(등급 근거, 재생성 방법, 정석 삭제 가이드, 명령어 복사 버튼)
3. **정리 검토 화면**
   - 선택 항목 요약, 되돌릴 수 없는 항목 별도 강조, 최종 확인
4. **이력·복원 화면**
   - 격리 목록, 남은 보관 기간, 복원/즉시 비우기
5. **설정**
   - 스캔 루트, 제외 경로, 격리 기간, 규칙 켜기/끄기

### 9.2 GUI 원칙

- GUI는 CLI와 동일한 코어를 Wails 바인딩으로 호출한다. GUI 전용 로직을 두지 않는다.
- 스캔은 이벤트 스트림으로 점진 렌더링한다(수십만 항목에서도 UI가 멈추지 않도록 가상 스크롤).
- 위험한 동작일수록 클릭 수를 늘린다. Safe 일괄 정리는 1단계 확인, Review 항목은 항목별 확인.
- 웹 기능 의존 최소화: WebKit(macOS/Linux)과 WebView2(Windows) 차이를 고려해 Chromium 전용 API를 쓰지 않는다. 파일 드롭은 Wails 런타임 이벤트를 사용한다.

## 10. 아키텍처

```
┌────────────┐   ┌────────────────────┐
│  shed CLI  │   │  Shed GUI (Wails)  │
│  (cobra)   │   │  Frontend: TS + UI │
└─────┬──────┘   └─────────┬──────────┘
      │    Go API          │ Wails bindings / events
      └─────────┬──────────┘
        ┌───────▼────────┐
        │   core (Go)    │
        ├────────────────┤
        │ scanner        │ 병렬 순회, 마커 탐지, 크기 측정, 하드링크 dedupe
        │ rules          │ 선언형 규칙 로딩(임베드 + 사용자 정의)
        │ classifier     │ 규칙 + 안전 신호 → 등급/근거
        │ signals        │ git, lockfile, 프로세스 사용, 앱 번들 판정
        │ planner        │ 위험 대비 확보량 기준 정리 플랜
        │ executor       │ quarantine / trash / native command / permanent
        │ journal        │ 매니페스트, 이력, 복원
        │ platform       │ OS별 경로, 휴지통, 프로세스 조회
        └────────────────┘
```

### 10.1 기술 결정

- **순수 Go 우선, cgo 회피**: 크로스 컴파일과 단일 바이너리 배포를 유지한다. Windows 휴지통·프로세스 조회는 `syscall`/`x/sys/windows`로 처리하고, macOS도 가능한 한 CLI(`lsof` 등) 호출이나 순수 Go로 해결한다.
- **규칙은 데이터로 관리**: 규칙을 코드가 아닌 YAML로 정의해 바이너리에 임베드한다. 커뮤니티 기여와 사용자 정의가 쉬워진다.
- **저장소**: 이력·매니페스트는 JSON Lines로 시작한다. 규모가 커지면 순수 Go SQLite(`modernc.org/sqlite`) 도입을 검토한다.
- **Wails 버전**: v2(안정) vs v3(멀티 윈도우, 트레이, 프리릴리스) 중 M2 착수 시점에 결정한다. 메뉴바/트레이 기능(M3)을 고려하면 v3가 유력하다.

### 10.2 규칙 스키마 예시

```yaml
id: node.modules
name: Node.js dependencies
kind: project            # project | global
markers: [package.json]
targets: [node_modules]
tier: safe
downgrade_if:
  - no_lockfile: [package-lock.json, pnpm-lock.yaml, yarn.lock, bun.lockb]
    to: caution
require:
  - git_ignored          # git 저장소라면 ignore 상태여야 함
rebuild:
  command: "npm ci"      # lockfile에 따라 pnpm install / yarn install 로 치환
  cost: low
explain: >
  프로젝트 의존성 폴더입니다. lockfile이 있으면 동일한 버전으로 재설치됩니다.
```

```yaml
id: ollama.models
name: Ollama models
kind: global
paths:
  env: OLLAMA_MODELS
  darwin: ~/.ollama/models
  linux: ~/.ollama/models
  windows: "%UserProfile%\\.ollama\\models"
tier: caution
itemize: ollama list     # 모델 단위로 쪼개서 보여줌
clean:
  native: "ollama rm {name}"
rebuild:
  command: "ollama pull {name}"
  cost: high
```

## 11. 안전 가드레일 (필수)

1. **Protected 절대 규칙**: git 추적 파일, `.git`, 홈 루트의 사용자 문서 폴더(Documents, Desktop, Pictures 등), SSH/GPG 키, 설정 디렉터리는 어떤 경우에도 삭제하지 않는다.
2. **이중 검사**: 스캔 시점과 삭제 직전 두 번 안전 신호를 확인한다.
3. **경로 검증**: 삭제 대상은 반드시 규칙이 매칭한 경로의 하위여야 하며, 정규화 후 심볼릭 링크 탈출을 검사한다.
4. **사용 중 차단**: 실행 중인 프로세스가 사용하는 경로(cwd, 열린 파일)는 삭제하지 않는다.
5. **앱 내부 보호**: `.app` 번들, 설치된 Electron 앱 등 애플리케이션 내부 폴더는 제외한다.
6. **권한 상승 금지**: 기본적으로 sudo/관리자 권한을 요청하지 않는다. 권한 부족으로 측정 못 한 영역은 "측정 불가"로 표시한다.
7. **실패 시 원자성**: 격리 이동 중 실패하면 해당 항목을 원래 상태로 되돌리고 나머지 작업을 계속할지 묻는다.
8. **테스트**: 규칙마다 "지워야 하는 것 / 절대 지우면 안 되는 것" 픽스처 테스트를 필수로 둔다.

## 12. 비기능 요구사항

| 항목 | 요구사항 |
|---|---|
| 성능 | SSD 기준 홈 디렉터리(파일 100만 개) 첫 스캔 30초 이내, 재스캔 5초 이내 목표 |
| 메모리 | CLI 스캔 중 200MB 이하 |
| 바이너리 | CLI 20MB 이하, GUI 앱 30MB 이하 |
| 플랫폼 | macOS(Apple Silicon/Intel), Linux(x64/arm64), Windows(x64) |
| 프라이버시 | 기본 텔레메트리 없음. 모든 분석은 로컬. 옵트인 익명 통계만 고려 |
| 오프라인 | 네트워크 없이 모든 기능 동작 |
| 접근성 | GUI 키보드 전체 조작, 색상 외에 아이콘/텍스트로 등급 구분 |
| 국제화 | 영어 기본, 한국어 우선 지원 |

## 13. 배포

- **macOS**: Developer ID 서명 + 공증(notarization). Homebrew cask(GUI), Homebrew formula(CLI). Mac App Store는 샌드박스 제약으로 대상 외.
- **Windows**: winget, Scoop, GitHub Releases. 사용자층 확대 시 코드 서명 도입.
- **Linux**: GitHub Releases(tar.gz), AppImage(GUI), 배포판 패키지는 커뮤니티 기여로.
- **권한 안내**: macOS에서 `~/Library` 일부 영역 측정을 위해 Full Disk Access가 필요한 경우 `shed doctor`와 GUI 온보딩에서 안내한다.

## 14. 수익 모델 (가설)

- **CLI**: 전 기능 무료, 오픈소스(MIT 검토).
- **GUI 무료**: 스캔, 분류, 설명, 가이드, 개별 정리, 복원.
- **GUI Pro (일회성 $10~20)**: 추천 플랜 원클릭 정리, 정기 자동 정리, 메뉴바 용량 모니터링과 임계치 알림, 여러 스캔 결과 비교.
- 결제·라이선스는 Merchant of Record 서비스(Lemon Squeezy, Paddle 등) 사용.
- 오픈소스 CLI로 신뢰와 인지도를 쌓고, GUI 편의 기능으로 수익화한다.

## 15. 로드맵

| 마일스톤 | 범위 | 완료 기준 |
|---|---|---|
| **M0: 코어 + CLI (macOS)** | scanner, rules 20종, classifier, `scan/report/explain/clean --dry-run` | 본인 머신에서 오분류 0건, 스캔 30초 이내 |
| **M1: 안전한 정리** | quarantine, restore, history, native 명령 실행, 안전 신호 전체 | 픽스처 테스트 전 규칙 통과, 공개 베타(Homebrew) |
| **M2: 크로스 플랫폼** | Linux, Windows 경로·휴지통·프로세스 조회, 규칙 40종 | 3개 OS CI 통과 |
| **M3: GUI (Wails)** | 대시보드, 탐색, 상세 패널, 정리 검토, 이력 | GUI에서 CLI와 동일 결과 |
| **M4: 자동화 + Pro** | 추천 플랜, 정기 정리, 메뉴바/트레이, 라이선스 | 유료 전환 흐름 동작 |

## 16. 성공 지표

- **안전성**: 사용자가 보고한 "지우면 안 되는 걸 지움" 사고 0건 (최우선 지표)
- **효용**: 첫 실행 사용자당 평균 확보 용량 (목표 20GB 이상)
- **신뢰**: Caution/Review 항목에서 `explain` 또는 상세 패널을 본 뒤 정리한 비율
- **복원율**: 격리 후 복원 비율 (높으면 분류 품질 문제 신호)
- **채택**: GitHub 스타, Homebrew 설치 수, 재방문(재스캔) 비율
- **전환**: GUI 무료 → Pro 전환율

## 17. 리스크와 대응

| 리스크 | 영향 | 대응 |
|---|---|---|
| 오분류로 중요한 데이터 삭제 | 치명적, 신뢰 붕괴 | Protected 절대 규칙, 이중 검사, 격리 기본값, 규칙별 픽스처 테스트 |
| 도구 캐시 경로·구조 변경 | 규칙 누락/오작동 | 설정 조회 명령 우선, 규칙 버전 관리, 규칙만 별도 업데이트 가능한 구조 검토 |
| 격리가 공간을 즉시 확보하지 않음 | 사용자 혼란 | UI에 "격리 중 N GB, N일 후 확보" 명시, 즉시 비우기 버튼 |
| macOS 권한(Full Disk Access) | 일부 영역 측정 불가 | 측정 불가 영역을 명시적으로 표시, 권한 안내 |
| 무료 대체재(kondo, npkill, ncdu) | 유료화 어려움 | 전역 캐시·AI 모델 커버리지, 복원 가능 삭제, GUI 경험으로 차별화 |
| Wails 웹뷰 OS별 차이 | GUI 버그 | 표준 웹 기능만 사용, 3개 OS 수동 QA 체크리스트 |

## 18. 오픈 이슈

1. Go 모듈 캐시처럼 파일이 읽기 전용 권한으로 저장된 캐시를 격리 이동할 때의 권한 처리 방식
2. Docker Desktop VM 디스크 이미지는 내부를 정리해도 호스트 파일 크기가 바로 줄지 않는 문제를 어떻게 안내할지
3. "마지막 사용 시각" 판단 기준: 파일 mtime vs atime(대부분 noatime/relatime) vs git 마지막 커밋 시각
4. 규칙을 바이너리 업데이트 없이 갱신할 수 있게 할지(원격 규칙 피드) — 프라이버시·보안과의 트레이드오프
5. 모노레포(pnpm workspace, Turborepo)에서 루트와 하위 패키지의 node_modules를 어떻게 묶어서 보여줄지
6. 최종 제품명 및 CLI 명령어 이름 충돌 여부
