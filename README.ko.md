# CPAModelDock

[English](README.md) · [운영 안내](docs/operations.md) · [빌드 안내](docs/building.md) · [검증 근거](docs/verification.md) · [구현 계약](docs/design/contract.md) · [MIT 라이선스](LICENSE)

CPAModelDock은 [CLIProxyAPI(CPA)](https://github.com/router-for-me/CLIProxyAPI/tree/v8.0.44)의 네이티브 플러그인입니다. 고정된 모델 별칭, 모델 허용 목록, 같은 모델 계열 안에서의 보수적인 자동 업그레이드를 제공합니다. 클라이언트는 `gpt-sol`을 요청하고 운영자는 대상을 `gpt-6.1-sol`로 고정하거나, 실제 라우팅 가능한 더 높은 Sol 버전으로 자동 변경할 수 있습니다. Model Dock에는 영어/한국어 관리 UI, 모델 목록과 정책 미리보기, 판정 설명, 영속 변경 이력이 포함됩니다.

플러그인 ID는 `cpa-model-dock`, 표시 이름은 `CPAModelDock`입니다. 모듈 경로 `github.com/cpa-model-dock/cpa-model-dock`의 소유자는 **임시 이름**입니다. 실제 공개 릴리스 저장소나 공식 스토어 등록을 뜻하지 않습니다. 예제 모델 이름은 설명용 테스트 데이터이며 제공자의 실제 출시·이용 가능 여부를 나타내지 않습니다.

## 호환성과 검증 범위

테스트 기준 호스트는 **CPA v8.0.44**, 네이티브 **ABI 1**, 협상 가능한 플러그인 JSON 스키마는 **6–7**입니다. 같은 ABI를 쓰는 모든 호스트의 호환성을 보증하지 않습니다. 필요한 관리·리소스 API가 로컬에서 확인한 v8.0.37 소스에도 있지만, **v8.0.37을 테스트된 최소 버전으로 주장하지 않습니다**. 근거: [CPA ABI 상수](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/sdk/pluginabi/types.go), [v8.0.37 API 선언](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.37/sdk/pluginapi/types.go), [등록 구현](dispatch.go), [ABI 테스트](dispatch_test.go).

Darwin amd64/arm64, Linux amd64/arm64, Windows amd64의 다섯 플랫폼 빌드가 통과했습니다. Go/ABI 검사, Linux arm64 ABI 스모크 테스트, Node 테스트 38개도 통과했습니다. 정확한 환경, 산출물 크기·해시와 제한은 [검증 문서](docs/verification.md), [빌드 문서](docs/building.md)에 있습니다. Linux 산출물은 **glibc ≥2.34**가 필요하며, Debian에서 만든 이 바이너리는 musl 전용 Alpine을 지원하지 않습니다. Windows에서의 네이티브 로드는 테스트하지 않았습니다.

실제 CPA v8.0.44 관리 API에서 네 예제 모두 `POST /validate`가 `valid:true`를 반환했고, `PUT /state`로 리비전 1–4를 저장·영속화했습니다. 테스트 호스트에는 모델 8개가 등록됐지만 라우팅 가능한 자격 증명 수가 0이어서 미리보기는 비어 있었고 자동 확인은 `no_routable_candidates`를 반환했습니다. 이는 **검증과 저장만 확인한 결과**이며 추론 라우팅이나 실제 업그레이드 성공을 증명하지 않습니다. 숨긴 원본의 explain 결과는 `hidden_by_alias`, 상태 404였습니다. 보존 보고서와 재현 방법은 [검증 문서](docs/verification.md)를 참고하세요.

<!-- E2E: 실제 플러그인 추론 라우팅, JSON/SSE 응답 모델 치환, 자동 대상 업그레이드, 재시작 복구의 통합 검증 근거 추가 필요. 관리 API 검증·저장만으로 입증할 수 없음. -->

## 설치와 Model Dock 열기

### 수동 설치

1. CPA의 운영체제·CPU와 맞는 신뢰할 수 있는 ZIP을 받거나 직접 빌드합니다. 신뢰하는 배포자의 `SHA256SUMS`와 SHA-256을 대조합니다.
2. 수동 교체 시 CPA를 중지합니다. ZIP **루트에 있는** 라이브러리를 `plugins.dir`에 풉니다. macOS는 `cpa-model-dock.dylib`, Linux는 `cpa-model-dock.so`, Windows는 `cpa-model-dock.dll`입니다. ID를 바꾸거나 ZIP 자체를 라이브러리로 설치하지 마세요. 검색 경로에 충돌하는 이전 사본이 없도록 정리합니다.
3. 기존 CPA 설정에 아래 블록을 추가하되 서버·관리 인증·제공자 설정은 유지합니다. CPA 서비스 계정이 쓸 수 있는 상태 디렉터리를 만듭니다.
4. CPA를 시작하고 관리 센터의 설치된 플러그인 목록에서 `cpa-model-dock`의 로드·활성 상태를 확인한 뒤 플러그인 메뉴 **Model Dock**을 엽니다.

CPA는 플러그인 루트와 플랫폼 하위 디렉터리를 지원하며, 스토어 설치는 버전이 붙은 파일명을 사용합니다. 전역·개별 활성화 스위치가 모두 필요합니다. C 함수 테이블을 사용하는 네이티브 라이브러리이므로 OS/CPU, 네이티브 ABI, 지원 JSON 스키마, C 컴파일러·런타임 의존성의 호환성을 모두 확인해야 합니다. ABI 번호만 같다고 충분하지 않습니다. 근거: [검색 로직](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/internal/pluginhost/platform.go), [Unix 로더](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/internal/pluginhost/loader_unix.go), [Windows 로더](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/internal/pluginhost/loader_windows.go).

```yaml
plugins:
  enabled: true
  dir: plugins
  configs:
    cpa-model-dock:
      enabled: true
      priority: 1000
      state-dir: /var/lib/cliproxyapi/cpa-model-dock
      catalog-refresh-interval: 5m
      guard-uncovered-endpoints: true
```

의도한 정책 실행 순서를 위해 설치된 플러그인 중 **유일한 최고 우선순위**를 지정하세요. `1000`은 예시이며 예약된 최댓값이 아닙니다. CPA는 우선순위 내림차순, 동률이면 ID 오름차순으로 기능을 정렬합니다. 우선순위로 모든 경로의 보안을 보장하거나 프런트엔드 인증 순서를 바꿀 수는 없습니다. 근거: [호스트 설정](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/internal/pluginhost/config.go), [기능 정렬](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/internal/pluginhost/snapshot.go).

| 설정 | 실제 동작·기본값 |
| --- | --- |
| `plugins.enabled` / 개별 `enabled` | CPA 스위치이며 둘 다 true여야 합니다. 생략하면 비활성입니다. |
| `plugins.dir` | 기본 `plugins`. 상대 경로는 CPA 작업 디렉터리 기준이며 호스트가 맨 앞의 `~`를 확장합니다. |
| `plugins.store-sources` | 기본 `[]`. 추가 레지스트리 URL 목록이며 공식 레지스트리는 항상 포함됩니다. |
| `priority` | CPA 실행 순서. 생략하면 0. 다른 플러그인보다 유일하게 높은 값을 사용합니다. |
| `state-dir` | 플러그인 소유의 절대 경로. 비어 있거나 생략하면 **CPA 프로세스 사용자**의 `$HOME/.cli-proxy-api/cpa-model-dock`. 상대 경로는 거부합니다. |
| `catalog-refresh-interval` | Go duration 형식. 기본 `5m`, 최소 `30s`. |
| `guard-uncovered-endpoints` | 기본 true. 아래 설명한 제한적인 보조 방어입니다. |

모델 정책은 별도 YAML 키가 아닌 플러그인 상태/API에서 관리합니다. `settings.refresh_interval_seconds > 0`이면 YAML 주기보다 우선하고, `0`이면 YAML을 사용합니다. 초기 상태는 300이므로 YAML에 지정한 다른 주기를 사용하려면 이 값을 0으로 설정해야 합니다. 근거: [설정 파서·폴링](app.go), [상태 기본값](internal/policy/policy.go).

### 스토어 설치와 자체 레지스트리

선택한 레지스트리에 플러그인이 **실제로 게시된 뒤** CPA 관리 센터의 스토어에서 `cpa-model-dock`을 설치할 수 있습니다. 공개 스토어 등록을 약속하지 않습니다. 자체 호스팅하려면 올바른 다운로드 기본 URL로 패키징하고 기존 `plugins` 설정에 레지스트리 URL을 추가합니다.

```yaml
plugins:
  enabled: true
  dir: plugins
  store-sources:
    - https://plugins.example.com/model-dock/registry.json
  configs:
    cpa-model-dock:
      enabled: true
      priority: 1000
      state-dir: /var/lib/cliproxyapi/cpa-model-dock
      catalog-refresh-interval: 5m
      guard-uncovered-endpoints: true
```

예시 도메인은 실제 주소로 바꾸세요. 생성된 스키마 2 직접 설치용 `registry.json`과 ZIP을 지정 URL에 게시합니다. 레지스트리에는 플랫폼, URL, SHA-256, 크기가 들어갑니다. 기본 `BASE_URL`은 `example.invalid`여서 실제 다운로드용으로 쓸 수 없습니다. 스토어는 무결성을 검사하고 플랫폼 라이브러리를 설치하며 개별 인스턴스를 활성화하지만 **전역 `plugins.enabled`는 활성화하지 않습니다**. 설정 편집 시 스토어가 관리하는 `store` 매핑을 보존하세요. 체크섬은 게시자 서명이나 보안 샌드박스가 아닙니다.

향후 GitHub 릴리스 방식으로 등록한다면 CPA의 GitHub 설치기는 `checksums.txt`를 요구합니다. `SHA256SUMS`만으로는 부족합니다. 패키저가 같은 내용의 `checksums.txt`도 생성하므로 함께 게시하거나 생성된 직접 설치 레지스트리를 사용하세요. 근거: [패키징](packaging/README.md), [레지스트리 스키마](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/internal/pluginstore/registry.go), [압축 설치기](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/internal/pluginstore/install.go), [GitHub 파일 선택](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/internal/pluginstore/github.go), [스토어 관리](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/internal/api/handlers/management/plugin_store.go).

### 업데이트·비활성화·제거

버전 변경 전에 `state-dir`을 백업하세요. 스토어 업데이트는 원하는 버전으로 같은 설치 작업을 수행합니다. 수동 업데이트는 CPA를 중지한 뒤 맞는 라이브러리로 교체합니다. 정책·이력을 유지하려면 `state-dir`을 그대로 사용하고 재시작·재로드 후 로드 버전과 정책을 확인하세요. Windows에서 로드된 동일 버전 산출물을 교체할 때는 재시작이 필요할 수 있습니다.

개별 활성화 스위치로 끄거나 CPA 플러그인 목록에서 제거할 수 있습니다(`DELETE /v8/management/plugins/cpa-model-dock`). 수동 제거는 CPA를 중지하고 라이브러리·설정 항목을 지웁니다. **플러그인 상태 디렉터리는 남습니다.** 정책·이력을 지우려는 경우에만 별도로 삭제하세요. 비활성화·제거·언로드·퓨즈 차단 상태에서는 정책을 집행하지 못합니다. 네이티브 Go 라이브러리 이미지는 CPA 종료 전까지 메모리에 남을 수 있습니다. 근거: [제거 핸들러](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/internal/api/handlers/management/plugins.go), [로더 수명](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/internal/pluginhost/loader_unix.go).

### UI와 관리 키

메뉴가 여는 정확한 리소스 URL은 다음과 같습니다. 같은 CPA origin에서 직접 열어도 됩니다.

```text
/v0/resource/plugins/cpa-model-dock/index.html
```

내장 정적 파일 네 개(`index.html`, `app.js`, `app.css`, `i18n.js`)는 공개 리소스이며 상태나 자격 증명을 포함하지 않습니다. 정책 요청은 모두 인증이 필요한 `/v0/management/plugins/cpa-model-dock`으로 갑니다. 추론 API 키가 아닌 **CPA 관리 키**를 사용하세요. 공개 HTML이 열린다고 관리 인증 성공을 뜻하지는 않습니다.

UI는 관리 센터의 기존 `cli-proxy-auth` 또는 구형 `managementKey` 저장소를 읽기만 합니다. 직접 입력한 키는 **이 탭에서 기억하기**를 선택하지 않으면 메모리에만 남습니다. 선택하면 `sessionStorage['cpa-model-dock.tab-key']`에 저장하며 관리 센터 키보다 우선합니다. 이 UI가 localStorage에 쓰는 값은 언어 선택뿐입니다. 키는 URL·로그가 아닌 Authorization 헤더로 전송하며 화면에 표시하는 API 데이터·오류에서 알려진 키 문자열을 가립니다. 요청은 `location.origin`에 한정하고 저장된 `apiBase`는 무시하며 리다이렉트도 거부합니다. 저장소 접근 실패 시 비밀번호 입력란을 사용합니다. 이는 플러그인 UI의 동작이며 CPA의 모든 페이지, 다른 브라우저 코드, 네이티브 파일까지 보장하는 설명은 아닙니다. 근거: [UI](web/assets/app.js), [리소스·CSP](internal/mgmt/handler.go), [CPA 리소스 디스패치](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/internal/pluginhost/management.go).

개요는 저장된 정책을 보여 줍니다. 별칭·허용 목록 탭은 초안을 편집하며 **변경 저장**은 검증 후 저장하고 **변경 취소**는 확인 후 초안을 버립니다. 별칭 확인과 이력 복원 전에는 초안을 저장하거나 취소해야 합니다. 모델 목록 새로고침은 편집 내용을 유지하지만 동시 정책 변경 시 리비전 충돌이 날 수 있습니다. UI에서 English 또는 한국어를 선택할 수 있습니다.

<!-- E2E: 백엔드/UI 통합 후 실제 관리 센터 안의 UI, 인증 요청, CSP, 키 교체, 충돌 처리 확인 필요. -->

## 정책 순서와 자동 업그레이드

[계약 §3](docs/design/contract.md#3-policy-semantics-single-engine-for-listing-and-requests), [정책 엔진](internal/policy/policy.go), [훅](internal/hooks/hooks.go)의 판정 순서는 다음과 같습니다.

1. 유효한 마지막 정책 없이 상태가 손상되거나 로드 불가능하면 `policy_unavailable`로 거부하고 공개 목록을 비웁니다.
2. 활성 별칭이면, 허용 목록 사용 시 해당 별칭의 활성 **별칭 규칙**이 필요합니다. 없으면 `alias_not_published`로 거부합니다. 게시된 별칭은 원본이 숨겨져 있어도 내부적으로 대상에 연결합니다. 대상이 라우팅 불가능하면 503 `alias_target_unavailable`이며 다른 모델로 우회하지 않습니다.
3. 활성 별칭의 `expose_original:false` 대상 이름은 숨기고 직접 요청을 거부합니다(`hidden_by_alias`, HTTP 404). 단, **활성 exact 규칙으로 그 이름을 명시적으로 허용하면 예외**입니다. 계열·패턴 규칙은 숨김을 해제하지 않습니다.
4. 허용 목록을 끈 상태라면 나머지 이름을 허용합니다.
5. 허용 목록을 켠 상태라면 활성 exact/family/pattern 규칙과 일치하는 이름만 허용합니다. 활성 규칙이 하나도 없으면 모두 거부합니다. exact 규칙은 자동 확장되지 않고 alias 규칙은 별칭만 게시합니다.

공개 목록에는 허용되면서 라우팅 가능한 모델과, 라우팅 가능한 대상으로부터 복제한 게시 별칭만 나타납니다. 목록 필터링 자체가 독립적인 인증 경계는 아닙니다. 대표 추론 오류는 403 `model_not_allowed`, 404 `model_not_found`, 503 `alias_target_unavailable`/`policy_unavailable`이며 explain의 reason code는 더 구체적인 정책 이유입니다.

비교 시 공백을 다듬고 대소문자를 무시하며 끝의 thinking 접미사 `(...)`와 Gemini `models/`를 제거합니다. 자격 증명 접두사는 **호스트가 명시적으로 보고했을 때만** 제거합니다. 기본 v8.0.44 런타임 DTO에는 그 정보가 없어 신뢰하는 접두사 집합이 비어 있습니다. 임의의 `p/model`은 그대로 비교합니다. 이 정규화는 비교용이며 모든 전달 이름을 일괄 변경하는 기능이 아닙니다. CPA가 자격 증명과 네이티브 별칭·풀을 선택한 뒤 **AfterAuth에서 최종 해석된 모델을 다시 검사**합니다. 별칭은 설정한 대상과 같아야 하고, 일반 요청의 대상도 숨김·허용 규칙을 통과해야 합니다. CPA가 매핑 출처를 제공하지 않아 다른 대상에 대한 네이티브 별칭 매핑은 보수적으로 거부합니다. `oauth-model-alias`, 제공자 풀, 접두사가 안전하게 검사를 우회한다고 가정하지 마세요. 근거: [목록 투영](internal/catalog/catalog.go), [CPA 모델 해석](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/sdk/cliproxy/auth/conductor_models.go), [AfterAuth 실행](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/sdk/cliproxy/auth/conductor_execution.go).

자동 별칭은 계열 템플릿과 **모델 이름 전체**를 비교합니다. `gpt-{version}-sol`은 `gpt-6.1-sol`과 일치하지만 `gpt-6.1-sol-mini`와는 일치하지 않습니다. 버전을 숫자 구성 요소로 비교한 뒤(`6.10 > 6.9`, `4-5 == 4.5`) 비교 가능한 날짜를 비교합니다. `{date}`는 전체 날짜와 모호하지 않은 짧은 날짜를 지원하며, 불확실한 연도·세기를 임의로 추측하지 않습니다. 같은 버전의 날짜 있음/없음, 서로 다른 날짜 정밀도, 최상위 동률, 비교 불가능한 시험 버전은 모호할 수 있습니다. 이때 **현재 대상을 유지**하고 이유를 기록합니다. 같거나 낮은 버전으로 변경하지 않으며 현재 대상을 해석할 수 없으면 `current_unparseable`로 유지합니다. 시험 버전(`preview`, `beta`, `alpha`, `exp`, `experimental`, `rc`, `nightly`, `dev`, `test`)은 기본 제외됩니다. 계열 허용 규칙도 시험 버전을 제외합니다.

기본 계열은 Sol, Astra, Luna, Claude Opus/Sonnet/Haiku/Fable, Gemini Pro/Flash입니다. 사용자 계열은 `{version}` 하나와 선택적인 `{date}` 하나를 사용합니다. 활성 런타임 자격 증명이 제공하는 후보만 자동 선택합니다. 없거나 비활성·사용 불가인 자격 증명, 목록에만 있는 모델은 라우팅 가능한 것으로 취급하지 않습니다. 새로고침 실패 시 마지막 정상 정보와 대상을 유지합니다. 이 목록은 스케줄러의 현재 선택 가능성이나 모델별 쿨다운을 원자적으로 보증하지 못하며 정적 executor 전용 등록은 포함하지 않습니다. 상태 확인은 **기본 꺼짐**이며 켜면 변경 전 CPA를 통한 최소 유료 요청이 발생할 수 있습니다. dry run은 유료 상태 확인을 실행하지 않습니다. 근거: [버전 파서](internal/version/version.go), [자동 평가](internal/autoupdate/evaluate.go), [목록](internal/catalog/catalog.go), [상태 확인 요청](app.go).

## UI와 curl로 따라 하는 네 가지 예제

테스트 인스턴스에서 실행하거나 기존 정책 교체 전에 내용을 검토하세요. 각 예제는 부분 수정이 아닌 **전체 상태 교체**이며 관련 없는 별칭·규칙을 제거합니다. 별칭을 저장하려면 대상이 실제 호스트 목록에 있어야 하며, 알려져 있지만 잠시 라우팅 불가능한 대상은 가능합니다. 설명용 `gpt-6.1-sol`, `gpt-6-astra`, `claude-opus-4-5`를 필요한 테스트 제공자 설정에 등록하세요. [예제 안내](examples/README.md)를 참고하세요.

### 공통 curl 함수

curl과 jq가 필요합니다. 실제 키를 저장소 파일에 넣지 말고 로컬 셸 환경에 설정하세요. `CPA_MANAGEMENT_KEY`는 관리용, 아래 `CPA_API_KEY`는 별도 추론용입니다.

```sh
export CPA_ORIGIN=http://127.0.0.1:8317
# CPA_MANAGEMENT_KEY와 CPA_API_KEY는 환경에 비공개로 설정합니다.
export DOCK_API="$CPA_ORIGIN/v0/management/plugins/cpa-model-dock"

apply_example() (
  set -eu
  example_file=$1
  # 검증은 POST입니다. HTTP 실패 또는 valid:false이면 중단합니다.
  validation=$(curl --fail-with-body -sS -X POST "$DOCK_API/validate" \
    -H "Authorization: Bearer $CPA_MANAGEMENT_KEY" \
    -H 'Content-Type: application/json' --data-binary "@$example_file")
  printf '%s\n' "$validation" | jq .
  printf '%s\n' "$validation" | jq -e '.valid == true' >/dev/null
  # 상태 교체 직전에 현재 리비전을 읽습니다.
  current=$(curl --fail-with-body -sS "$DOCK_API/state" \
    -H "Authorization: Bearer $CPA_MANAGEMENT_KEY")
  revision=$(printf '%s\n' "$current" | jq -er '.revision')
  jq --argjson revision "$revision" '.revision = $revision' "$example_file" |
    curl --fail-with-body -sS -X PUT "$DOCK_API/state" \
      -H "Authorization: Bearer $CPA_MANAGEMENT_KEY" \
      -H 'Content-Type: application/json' --data-binary @-
)

explain_model() {
  jq -n --arg model "$1" '{model:$model}' |
    curl --fail-with-body -sS -X POST "$DOCK_API/explain" \
      -H "Authorization: Bearer $CPA_MANAGEMENT_KEY" \
      -H 'Content-Type: application/json' --data-binary @-
}
```

409는 다른 저장·자동 업데이트가 먼저 반영됐다는 뜻입니다. GET state로 변경 내용을 확인한 뒤 의도적으로 재검증·재적용하세요. 무조건 재시도하지 마세요. 예제의 revision `0`은 초기 자리표시자이므로 현재 리비전으로 재사용하면 안 됩니다. `POST /explain`은 실제 추론 없이 정책만 판정합니다. JSON의 `status`와 관리 요청 자체의 HTTP 상태는 다릅니다.

### 1. Sol 별칭을 고정 대상에 연결

**별칭 → 별칭 추가**에서 이름 `gpt-sol`, 방식 **고정**, 대상 `gpt-6.1-sol`을 지정합니다. 제공자는 비우고 **원래 모델 이름 허용**을 켜며 응답 치환은 기본 false로 둡니다. 허용 목록은 끄고 저장합니다. 별칭과 라우팅 가능한 원본이 모두 공개되는 구성을 의도합니다.

```sh
apply_example examples/01-pinned-sol.json
explain_model gpt-sol
curl --fail-with-body -sS "$CPA_ORIGIN/v1/chat/completions" \
  -H "Authorization: Bearer $CPA_API_KEY" -H 'Content-Type: application/json' \
  -d '{"model":"gpt-sol","messages":[{"role":"user","content":"Hello"}]}'
```

마지막 명령은 실제 추론이므로 제공자 요금이 발생할 수 있습니다. 라우팅 가능한 업스트림이 필요하며 예제 저장만으로 이를 확인할 수 없습니다.

<!-- E2E:pinned-sol 업스트림 호출 근거와 함께 gpt-6.1-sol 별칭 라우팅 확인 필요. -->

### 2. Sol 계열 안에서 자동 업그레이드

`gpt-sol`의 방식을 **자동**, 초기 대상을 `gpt-6.1-sol`, 계열을 **GPT Sol**로 설정합니다. 원래 이름을 허용하고 시험 버전은 끕니다. API 전용 `auto.health_check`는 기본 false로 유지하고 허용 목록도 끈 뒤 저장합니다. **지금 확인**으로 후보를 미리 보고 **확인 결과 적용**으로 다시 평가·적용합니다. 백그라운드 새로고침도 자동 별칭을 평가하므로 버튼을 눌렀을 때만 변경되는 것은 아닙니다.

```sh
apply_example examples/02-auto-sol.json
curl --fail-with-body -sS -X POST "$DOCK_API/aliases/check" \
  -H "Authorization: Bearer $CPA_MANAGEMENT_KEY" -H 'Content-Type: application/json' \
  -d '{"dry_run":true}'
revision=$(curl --fail-with-body -sS "$DOCK_API/state" \
  -H "Authorization: Bearer $CPA_MANAGEMENT_KEY" | jq -er '.revision')
jq -n --argjson revision "$revision" '{revision:$revision,dry_run:false}' |
  curl --fail-with-body -sS -X POST "$DOCK_API/aliases/check" \
    -H "Authorization: Bearer $CPA_MANAGEMENT_KEY" -H 'Content-Type: application/json' \
    --data-binary @-
```

라우팅 후보 없음, 모호한 순서, 상태 확인 실패, 현재 대상 해석 실패 시 `kept`가 올바른 결과일 수 있습니다. 임의의 대체 모델을 선택하면 안 됩니다.

<!-- E2E:auto-sol 라우팅 가능한 호스트 데이터로 숫자 버전 업그레이드, 시험 버전 제외, 모호성 유지, 다운그레이드 방지 확인 필요. -->

### 3. Sol·Astra·Claude Opus 계열만 허용

관련 없는 초안 별칭·규칙을 지웁니다. **허용 목록**에서 활성 **모델 계열** 규칙 세 개를 추가해 **GPT Sol**, **GPT Astra**, **Claude Opus**를 선택하고 허용 목록을 켠 뒤 저장합니다. 이 예제는 **세 계열만** 사용합니다. 라우팅 가능한 `gpt-6.1-sol`, `gpt-6-astra`, `claude-opus-4-5`와 향후 같은 전체 이름 패턴의 안정 버전을 허용하도록 구성합니다. mini 변형과 다른 계열은 제외됩니다.

```sh
apply_example examples/03-family-whitelist.json
explain_model gpt-6.1-sol
explain_model gpt-6-astra
explain_model claude-opus-4-5
explain_model gpt-6.1-sol-mini
curl --fail-with-body -sS "$CPA_ORIGIN/v1/models" \
  -H "Authorization: Bearer $CPA_API_KEY"
```

<!-- E2E:family-whitelist Sol/Astra/Opus 목록 및 직접 요청 집행 확인 필요. explain만으로 추론 집행을 입증할 수 없음. -->

### 4. 원본은 숨기고 별칭만 공개

활성 고정 별칭 `gpt-sol → gpt-6.1-sol`을 만듭니다. **원래 모델 이름 허용**을 끄고(`expose_original:false`), 전역 **기본적으로 응답에 별칭 이름 사용**은 켜며 별칭별 설정은 **기본값 사용**으로 둡니다. **허용 목록**을 켜고 다른 규칙을 지운 뒤 `gpt-sol`에 대한 활성 **별칭** 규칙 하나만 추가해 저장합니다. 대상이 라우팅 가능하면 이 별칭만 공개됩니다. 내부 대상 실행은 허용하지만 원본 직접 호출은 숨김/404가 됩니다. 대상 exact 규칙을 추가하면 숨김을 해제하므로 추가하지 마세요.

```sh
apply_example examples/04-aliases-only.json
explain_model gpt-sol
explain_model gpt-6.1-sol
curl --fail-with-body -sS "$CPA_ORIGIN/v1/models" \
  -H "Authorization: Bearer $CPA_API_KEY"
# 지원되는 추론 경로에서 숨긴 모델 거부를 기대합니다.
curl -sS -i "$CPA_ORIGIN/v1/chat/completions" \
  -H "Authorization: Bearer $CPA_API_KEY" -H 'Content-Type: application/json' \
  -d '{"model":"gpt-6.1-sol","messages":[{"role":"user","content":"Hello"}]}'
```

<!-- E2E:aliases-only 별칭만 있는 목록, 별칭 추론 성공, 숨긴 원본의 업스트림 호출 없음, JSON/SSE 응답 이름 치환 확인 필요. -->

## 상태와 관리 API

저장 스키마는 **1**입니다. 초기 정책은 revision 0, 빈 families/aliases/rules, whitelist false, response rewrite false, 새로고침 300초입니다. `revision`, `updated_at`은 서버가 관리하며 GET state는 읽기 전용 기본 계열과 별칭 상태도 반환합니다. `settings`, `families`, `aliases`, `rules` 전체를 저장하세요. 런타임 표시 필드는 정책 설정이 아닙니다.

| 필드 | 의미·실제 디코딩 |
| --- | --- |
| `settings.whitelist_enabled` | 불리언, 초기 false. |
| `settings.rewrite_response_model` | 불리언, 초기 false. 별칭별로 재정의 가능. |
| `settings.refresh_interval_seconds` | 0이면 호스트 설정, 그 외 정수 ≥30. 초기 300. |
| `families[]` | `id`, `name`, `template`, 선택 `provider`, 명시적 `allow_dated`. 기본 계열은 읽기 전용. |
| `aliases[]` | `id`, `name`, `enabled`, `mode` (`pinned`/`auto`), 필수 `target`, `provider`, `expose_original`, nullable `rewrite_response_model`, `auto`. |
| `aliases[].auto` | `family`, `include_prerelease`, `health_check`. 불리언 기본 false. |
| `rules[]` | `id`, `enabled`, `kind` (`exact`/`family`/`pattern`/`alias`), `value`, 선택 `note`. 패턴은 정규식이 아닌 `*`, `?` 지원. |

**불리언은 명시적으로 보내세요.** Go 전체 상태 디코더는 생략된 불리언을 zero value로 처리합니다. mock의 기본값처럼 `enabled:true`나 사용자 계열의 `allow_dated:true`를 추론하지 않습니다. provider 생략은 빈 값이며 별칭 rewrite 생략/null은 전역 기본값을 상속합니다. 전체 교체에는 네 최상위 정책 필드가 모두 필요합니다. 새 별칭의 `id`는 생략해 서버가 생성하게 하고, 수정 시 반환받은 ID를 유지합니다. 사용자 계열·규칙 ID는 같은 요청의 참조를 위해 지정할 수 있으며 모든 ID는 고유해야 합니다. 별칭은 대소문자 무시 기준으로 실제 모델·다른 별칭과 충돌할 수 없고 대상은 다른 별칭이 아닌 알려진 실제 모델 ID여야 합니다. 근거: [타입](internal/policy/types.go), [입력 처리](internal/mgmt/input.go), [검증](internal/policy/validate.go), [예제](examples/README.md).

아래는 `/v0/management/plugins/cpa-model-dock` 기준 상대 경로입니다. 모두 관리 키가 필요합니다.

| 메서드·경로 | 목적 |
| --- | --- |
| `GET /overview`, `GET /state`, `GET /catalog` | 저장 정책, 현재 리비전, 상태와 모델 목록. |
| `PUT /state` | 전체 정책 검증·교체. 리비전 필요. |
| `POST /validate` | 저장 없는 전체 초안 검증·미리보기. **PUT은 미지원.** |
| `POST /catalog/refresh` | 목록 새로고침·자동 별칭 평가. 리비전 필요. |
| `POST /aliases/check` | `dry_run:true`면 미리보기, 아니면 리비전을 사용해 적용. 선택 `alias_id`. |
| `POST /rules/preview` | 초안의 규칙과 공개 이름 미리보기. |
| `POST /explain` | `{model,draft?}` 정책 설명. 추론 없음. |
| `GET /history?limit=100&before=...` | 최신순 이력. `before`는 해당 항목을 제외하는 이력 ID 커서. |
| `POST /history/restore` | `{entry_id,revision,scope:"state"|"alias_target"}`. 선택 항목의 **after** 재적용. |

변경 요청은 현재 body `revision` 또는 따옴표가 있는 `If-Match: "<n>"`를 사용합니다. 둘 다 보내면 일치해야 합니다. 읽기 전용 preview/validate/explain과 dry-run check는 리비전이 필요 없습니다. 잘못된 저장은 422이며 아무것도 변경하지 않습니다. `/validate`의 정책 검증 결과는 HTTP 200과 `valid:true` 또는 `valid:false`로 반환합니다. 오래된 리비전은 409입니다. 새로고침·변경 없는 확인을 포함해 리비전 증가를 예측하지 말고 변경 후 GET state로 다시 읽으세요. 백엔드/UI 통합 과정에서 런타임 상태·미리보기 응답 형태가 바뀔 수 있습니다. 개발 mock보다 구현 계약과 현재 백엔드를 우선합니다. 근거: [라우트](internal/mgmt/handler.go), [응답 뷰](internal/mgmt/views.go), [애플리케이션](app.go).

이력 복원은 **“이 변경 취소”가 아닙니다**. 선택한 항목의 `after` 정책을 새 리비전으로 다시 적용합니다. `alias_target` 범위는 아직 존재하는 별칭의 변경된 대상만 재적용합니다. 더 이전 상태로 돌아가려면 원하는 정책이 **After**에 있는 항목을 선택하세요. “Before”라고 설명하는 이전 mock 문서와 UI 도움말은 오래된 내용입니다. diff를 확인하고 백엔드 동작을 기준으로 판단하세요. 저장은 원자적 쓰기, 마지막 정상 사본 `state.json.bak`, 최대 1000개 `history.jsonl`을 사용합니다. 로드는 원본 → 백업 순서이며 둘 다 없을 때만 기본값을 씁니다. 기존 파일이 손상되고 유효한 백업도 없으면 `policy_unavailable`입니다. 상태 디렉터리당 writer 하나만 사용하세요. 프로세스 간 잠금은 없습니다. [저장소](internal/store/store.go), [운영 안내](docs/operations.md)를 참고하세요.

## 집행 한계와 개인정보

**모든 경로에 대한 보안 보장은 아닙니다.** 유일한 최고 플러그인 우선순위, 신뢰하는 네이티브 코드, 미지원 경로에 대한 필수 프록시 제한을 함께 사용하세요. 클라이언트가 프록시를 우회하지 못하도록 CPA 직접 접근을 제한하세요. [운영 안내](docs/operations.md)에 nginx/Caddy 설정 예제가 있습니다.

| 경계 | v8.0.44의 실제 동작 |
| --- | --- |
| Realtime/live | 모델 훅이 없습니다. 비독점 guard가 호출되면 whitelist 사용 시 거부할 수 있지만 기본 시작 순서는 config-access가 먼저이며 첫 인증 성공 시 종료합니다. 유효한 기본 클라이언트 키는 guard를 우회할 수 있고 realtime client secret도 fallback provider를 우회합니다. `/v1/realtime*`, `/v1/live*`를 외부에서 차단하세요. |
| Responses WebSocket | 진행 중 continuation 프레임은 ModelRouter/BeforeAuth/AfterAuth를 재실행하지 않습니다. create/append의 명시적 모델 변경은 조용히 허용하지 않고 replay를 요구합니다. steer는 바인딩된 모델로 인증하지만 제어 필드는 finalizer·업스트림 검증으로 넘깁니다. 엄격한 배포는 GET `/v1/responses`, GET `/backend-api/codex/responses`를 차단하세요. |
| Gemini 모델 상세 | GET `/v1beta/models/{id}`는 필터링된 모델 목록 훅을 거치지 않습니다. 상세 GET을 별도로 제한하세요. POST 추론은 다른 메서드 경로입니다. |
| 관리 원시 요청 | POST `/v0/management/api-call`, POST `/v8/management/requests/api-call`은 모델 정책 파이프라인을 사용하지 않습니다. 이 권한 높은 전송 경로를 제한하세요. |
| 비디오 콘텐츠 | GET `/openai/v1/videos/{video_id}/content`의 메타데이터·상태 조회는 훅을 거치지만 이후 바이너리 다운로드·복사는 모델 응답·스트림 훅 외부의 HTTP 클라이언트를 사용합니다. |
| 최종 payload 규칙 | 제공자 정규화와 설정된 기본값·덮어쓰기·필터가 **AfterAuth 이후** 실행될 수 있습니다. 모든 최종 업스트림 바이트를 플러그인이 승인하지는 않습니다. |
| 다른 플러그인·로드 상태 | 더 높은 우선순위 router/interceptor, 네이티브 executor, 비활성·미로드·언로드·퓨즈 차단, 호스트 변경은 신뢰 경계입니다. 우선순위만으로 집행을 보장하지 못합니다. |
| 진행 중 요청 | 정책·목록 스냅샷을 고정하므로 나중의 정책 철회가 이미 허용된 요청을 소급 취소하지 않습니다. 완료 또는 10분 비활동 후 pin이 만료됩니다. |

Codex Alpha Search는 일반 요청 훅 대신 ModelRouter를 사용합니다. 거부할 요청에는 플러그인의 등록된 정적 거부 executor를 선택하고, CPA가 이 비제공자 Alpha 경로를 503으로 거부합니다. 허용 별칭은 호환되고 라우팅 가능한 Codex 대상이 필요합니다. 존재하지 않는 제공자 이름을 거부용으로 사용하면 CPA가 그 경로를 무시할 수 있습니다. 엄격한 배포에서는 운영 안내처럼 Alpha Search 두 경로도 차단할 수 있습니다.

요청 훅은 성공 응답 안의 `Terminate`로 거부합니다. 일반 훅 오류를 반환하면 CPA가 계속 진행할 수 있기 때문입니다. 응답·스트림 DTO는 HTTP 상태 변경이나 열린 소켓 종료를 할 수 없으므로 복구 시 본문 정리·청크 삭제는 가능해도 전송 단계에서 소급 거부할 수는 없습니다.

소스 근거: [라우트 등록](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/internal/api/server_routes.go), [v0 관리](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/internal/api/server_management.go), [v8 관리](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/internal/api/server_management_v8.go), [인증 제공자 순서](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/sdk/cliproxy/builder.go), [첫 성공 인증](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/sdk/access/manager.go), [client-secret 미들웨어](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/internal/api/server_middleware.go), [WS continuation](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/internal/runtime/executor/codex_websockets_duplex.go), [비디오 다운로드](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/sdk/api/handlers/openai/openai_videos_handlers.go), [payload 규칙](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/internal/runtime/executor/openai_compat_executor.go), [플러그인 훅](internal/hooks/hooks.go).

이 플러그인의 Go 코드와 내장 웹 자산을 검토한 결과 **텔레메트리 코드나 외부 분석 엔드포인트는 없었습니다**. 모델 목록 폴링은 CPA 호스트 콜백을, 선택적인 유료 상태 확인은 CPA 모델 실행을 사용합니다. 목록 응답은 정리된 모델·제공자·개수·라우팅 가능 여부만 투영하며 업스트림 토큰·키·원본 인증 파일·자격 증명의 이메일 필드를 노출하지 않습니다. 플러그인 UI/API는 자격 증명 비밀을 노출하지 않으며 관리 키는 위에서 설명한 브라우저 저장 범위 안에서 인증에 사용합니다. 이 설명은 CPA의 다른 관리 API·로그·네이티브 인증 파일이나 다른 플러그인까지 포함하지 않습니다. 네이티브 플러그인은 CPA 프로세스 안에서 같은 권한으로 실행되므로 신뢰할 수 있어야 합니다. 근거: [목록 투영](internal/catalog/catalog.go), [관리 API](internal/mgmt/handler.go), [UI](web/assets/app.js), [호스트 콜백·상태 확인](app.go).

## 빌드·테스트·문제 해결

Go **1.26.10**, cgo, 대상에 맞는 C 툴체인이 필요하며 패키징은 Python 3을 사용합니다. UI mock·테스트는 **Node 24** 내장 모듈만 사용하고 npm 설치나 웹 번들 단계가 없습니다. 이 프로젝트 작업을 위해 `node_modules`를 읽거나 의존할 필요가 없습니다.

```sh
export GOTOOLCHAIN=go1.26.10
# macOS:
export CC=/usr/bin/clang
make test
make build
python3 scripts/abi_smoke.py dist/cpa-model-dock.dylib

make dist                       # 다섯 플랫폼 전체
make dist-darwin-amd64
make dist-darwin-arm64
make dist-linux-amd64
make dist-linux-arm64
make dist-windows-amd64
make package                    # 선택한 플랫폼 빌드 후 패키징
make package PACKAGE_ONLY=1     # 기존 산출물만 패키징
make package PLATFORMS='darwin/arm64 linux/amd64' \
  BASE_URL=https://plugins.example.com/model-dock/0.1.0

node --test web/dev/tests/*.test.mjs
node web/dev/mock-server.mjs
# http://127.0.0.1:8787/v0/resource/plugins/cpa-model-dock/index.html
# mock 관리 키: test-key

E2E_NO_PLUGIN=1 ./e2e/run.sh
CPA_MODEL_DOCK_LIB="$PWD/dist/cpa-model-dock.dylib" ./e2e/run.sh
```

`make build`는 호스트용 `dist/cpa-model-dock.<ext>`를 제공하고 플랫폼 빌드는 `dist/<os>/<arch>/cpa-model-dock.<ext>`를 만듭니다. 패키징은 ZIP 루트에 버전 없는 라이브러리가 들어간 `cpa-model-dock_0.1.0_<os>_<arch>.zip`, `SHA256SUMS`, 같은 내용의 `checksums.txt`, `registry.json`을 생성합니다. `PACKAGE_ONLY=1`은 선택한 산출물이 이미 있어야 하며 `PLATFORMS`로 범위를 제한할 수 있습니다. 크로스 빌드는 단순한 `GOOS`/`GOARCH` 변경뿐 아니라 [빌드 안내](docs/building.md)의 대상 컴파일러가 필요합니다. 제공된 전체 빌드는 Darwin에 macOS를, Linux/Windows에 Linux Docker 데몬을 사용합니다. 크로스 빌드 성공이 해당 플랫폼 네이티브 로드 성공을 증명하지는 않습니다.

캐시된 오프라인 모듈은 `GOCACHE=/tmp/cpa-model-dock-go-cache GOFLAGS=-mod=mod GOPROXY=off make test`로 사용할 수 있습니다. E2E는 로컬 리스너, 캐시된 의존성 또는 적절한 모듈 접근, cgo로 빌드한 CPA 바이너리, 플랫폼에 맞는 명시적 라이브러리 경로가 필요합니다. `E2E_NO_PLUGIN=1`은 플러그인 시나리오를 건너뜁니다. [E2E 안내](e2e/README.md), [mock 개발](web/dev/README.md), [검증 근거](docs/verification.md)를 참고하세요.

| 증상 | 확인 사항 |
| --- | --- |
| 플러그인·메뉴 없음 | 두 활성화 스위치, 파일명·디렉터리, OS/CPU, glibc/C 런타임, 로더 오류, 로드 버전, 우선순위. 정적 HTML만으로 정상 상태를 판단하지 마세요. |
| 관리 401/403 | 관리 키와 호스트 관리 접근 설정을 확인합니다. 추론 키로는 부족하며 만료된 탭 키는 교체합니다. |
| 저장 422 / 검증 `valid:false` | POST validate, 전체 필드, 명시적 불리언, 알려진 대상, 이름·ID 중복 여부, 새 별칭 ID 생략을 확인하고 오류 상세를 읽습니다. |
| 저장 409 | 현재 리비전을 읽고 초안을 조정합니다. 자동 업데이트가 먼저 저장했을 수 있습니다. |
| 빈 목록·별칭 503 | 허용 규칙, 별칭 게시, 대상 상태, 제공자 고정, 자격 증명 수를 확인합니다. 등록과 라우팅 가능은 다릅니다. |
| 접두사·네이티브 별칭 거부 | 기본 호스트는 신뢰할 접두사·매핑 출처를 제공하지 않습니다. 해석된 대상과 AfterAuth 정책을 확인합니다. |
| 자동 대상 유지 | 전체 계열 일치, 라우팅 가능 여부, 버전·날짜 모호성, 시험 버전 설정, 새로고침, 상태 확인 결과를 확인합니다. |
| UI 설명과 다른 복원 | 백엔드는 선택 항목의 **After**를 재적용합니다. “Before” 도움말은 오래된 내용입니다. |
| 상태 손상·쓰기 불가 | 서비스 사용자 권한을 확인하고 백업을 보존한 뒤 API로 복구합니다. 전체 거부를 없애려고 상태를 삭제하지 마세요. |
| Realtime/WS/상세 필터 미적용 | 프록시 제한을 적용·검증하세요. 문서화된 호스트 한계입니다. |

백업·복구와 게이트웨이 예제는 [운영 안내](docs/operations.md)에 있습니다. 통합 결과는 소스 검토, mock 테스트, 관리 API만의 검증 결과와 구분해서 기록하세요.
