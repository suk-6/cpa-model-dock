# CPAModelDock operations / 운영 안내

[English README](../README.md) · [한국어 README](../README.ko.md) · [Builds / 빌드](building.md) · [Verification / 검증](verification.md) · [Contract / 계약](design/contract.md)

## Deployment boundary / 배포 경계

**EN:** CPA v8.0.44 has routes and later processing stages outside this plugin's model hooks. Use the controls below as a deployment starting point, with a unique highest plugin priority and only trusted native plugins. They do not prove universal enforcement. Make the gateway mandatory: bind CPA to loopback or a private interface reachable only by the gateway/administrators, and restrict alternative listener addresses/ports. Use TLS and separate administrative access at your deployment boundary. Existing requests can retain pinned policy snapshots after a policy change. See the READMEs for authentication-order, AfterAuth, final payload, and unload/fuse limitations.

**KO:** CPA v8.0.44에는 플러그인의 모델 훅을 거치지 않는 경로와 후속 처리 단계가 있습니다. 아래 제어를 배포의 출발점으로 삼고 유일한 최고 플러그인 우선순위와 신뢰할 수 있는 네이티브 플러그인을 사용하세요. 이 설정만으로 모든 경로의 집행을 증명하지는 않습니다. CPA를 루프백 또는 게이트웨이·관리자만 접근하는 사설 인터페이스에 바인딩하고 다른 주소·포트 접근을 제한해 게이트웨이를 필수 경로로 만드세요. 배포 경계에서 TLS와 별도의 관리 접근 제어를 적용하세요. 정책 변경 후에도 이미 시작한 요청은 고정된 스냅샷을 유지할 수 있습니다. 인증 순서, AfterAuth, 최종 payload, 언로드·퓨즈 제한은 README에 설명돼 있습니다.

These paths were checked against local v8.0.44 source; proxy snippets themselves have not been integration-tested here. / 아래 경로는 로컬 v8.0.44 소스에서 확인했지만 프록시 설정 자체의 통합 테스트는 수행하지 않았습니다.

| Restriction / 제한 | Source / 근거 |
| --- | --- |
| All realtime/live paths, including sessions, client secrets, translations and call controls / 세션·client secret·번역·통화 제어를 포함한 realtime/live | [server_routes.go](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/internal/api/server_routes.go#L90), [authentication bypass order / 인증 순서](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/sdk/cliproxy/builder.go#L239) |
| GET `/v1/responses`, GET `/backend-api/codex/responses` (WS only; POST remains available / WS 차단, POST 유지) | [route registration](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/internal/api/server_routes.go#L86), [continuation frames](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/internal/runtime/executor/codex_websockets_duplex.go) |
| GET under `/v1beta/models/` (detail; exact list GET remains available / 상세 차단, 정확한 목록 GET 유지) | [Gemini dispatch](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/internal/api/server_routes.go#L130) |
| `/v0/management/api-call` and `/v8/management/requests/api-call` | [v0](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/internal/api/server_management.go#L73), [v8](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/internal/api/server_management_v8.go#L31) |
| GET `/openai/v1/videos/{id}/content` (metadata lookup uses hooks, binary transfer does not / 메타데이터는 훅 사용, 바이너리는 미사용) | [video handler](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/sdk/api/handlers/openai/openai_videos_handlers.go#L834) |
| Optional strict Alpha Search block: `/v1/alpha/search`, `/backend-api/codex/alpha/search` / 선택적인 엄격한 Alpha Search 차단 | [routes](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/internal/api/server_routes.go#L89), [plugin denial executor](../internal/hooks/hooks.go) |

**EN:** The examples deliberately block all GETs under the Gemini detail prefix, including GET-style streaming actions that share its wildcard route; POST generation and the exact `/v1beta/models` list are unaffected. The Alpha blocks are included for a strict deployment; omit those two blocks/matchers only if you intentionally use and independently validate the plugin's Alpha router coverage. These configurations are examples for a dedicated gateway; merge carefully with existing route ordering and stronger access rules.

**KO:** 예제는 Gemini 상세 접두사 아래의 모든 GET을 차단하므로 같은 와일드카드 경로를 사용하는 GET 방식 스트리밍도 차단됩니다. POST 생성과 정확한 `/v1beta/models` 목록은 유지합니다. 엄격한 배포를 위해 Alpha 차단도 포함했습니다. 플러그인의 Alpha router 집행을 별도로 검증하고 사용하려는 경우에만 해당 두 경로 차단을 제외하세요. 전용 게이트웨이 예제이므로 기존 라우트 순서와 더 강한 접근 제한을 고려해 통합하세요.

## nginx

**EN:** Dedicated loopback gateway on 8080 → CPA on 8317. Replace `10.20.30.0/24` with the actual admin network; management keys remain required. Put these sibling locations in one `server`. Exact `=` matches win first. Otherwise nginx chooses the longest prefix, then checks regex locations in file order and uses the first match; plain `location /` is the fallback. A longest matching `^~` prefix skips regex checking **regardless of where it appears in the file**. Do not add bypassing exact/`^~` locations or earlier broad regex proxies. The GET-only `if` returns 403 inside the Gemini location; POST stays in that same location and must have its own `proxy_pass` (it does not fall through to `/`). [nginx location rules](https://nginx.org/en/docs/http/ngx_http_core_module.html#location), [address restrictions](https://nginx.org/en/docs/http/ngx_http_access_module.html).

**KO:** 8080 루프백 게이트웨이 → 8317 CPA 예제입니다. `10.20.30.0/24`는 실제 관리자 네트워크로 바꾸세요. 관리 키도 여전히 필요합니다. 같은 `server`의 동급 location으로 배치합니다. 정확한 `=` 일치가 먼저 적용됩니다. 그 외에는 가장 긴 접두사를 선택한 후 파일 순서대로 첫 일치 정규식을 사용하며 일반 `location /`는 fallback입니다. 가장 긴 접두사가 `^~`이면 **파일 위치와 관계없이** 정규식 검사를 건너뜁니다. 차단을 우회하는 exact/`^~`나 앞선 광범위 정규식 프록시를 추가하지 마세요. Gemini location의 `if`는 GET만 403으로 반환합니다. POST는 `/`로 이동하지 않고 그 location에 남으므로 자체 `proxy_pass`가 필요합니다. 근거는 위 nginx 공식 문서입니다.

```nginx
server {
    listen 127.0.0.1:8080;
    server_name localhost;

    location ~ ^/v1/(realtime|live) {
        return 403;
    }
    location = /v0/management/api-call {
        allow 10.20.30.0/24;
        deny all;
        proxy_pass http://127.0.0.1:8317;
        proxy_http_version 1.1;
    }
    location = /v8/management/requests/api-call {
        allow 10.20.30.0/24;
        deny all;
        proxy_pass http://127.0.0.1:8317;
        proxy_http_version 1.1;
    }
    # Optional strict Alpha Search restrictions.
    location = /v1/alpha/search {
        return 403;
    }
    location = /backend-api/codex/alpha/search {
        return 403;
    }
    location ~ ^/(v1|backend-api/codex)/responses/?$ {
        if ($request_method = GET) { return 403; }
        proxy_pass http://127.0.0.1:8317;
        proxy_http_version 1.1;
        proxy_buffering off;
    }
    location ~ ^/v1beta/models/ {
        if ($request_method = GET) { return 403; }
        proxy_pass http://127.0.0.1:8317;
        proxy_http_version 1.1;
        proxy_buffering off;
    }
    location ~ ^/openai/v1/videos/[^/]+/content/?$ {
        if ($request_method = GET) { return 403; }
        proxy_pass http://127.0.0.1:8317;
        proxy_http_version 1.1;
        proxy_buffering off;
    }
    location / {
        proxy_pass http://127.0.0.1:8317;
        proxy_http_version 1.1;
        proxy_buffering off;
    }
}
```

## Caddy

**EN:** Equivalent loopback listener. Replace the admin CIDR as above. `remote_ip` checks the immediate peer; behind another proxy this is insufficient unless that peer is the intended access boundary. Do not treat untrusted forwarded headers as admin identity. `route` preserves order; the named Gemini matcher requires both GET and `/v1beta/models/*`, so POST `:generateContent`/`:streamGenerateContent` and exact GET `/v1beta/models` remain proxied. Adapt the site address to your TLS hostname. [Caddy matchers](https://caddyserver.com/docs/caddyfile/matchers), [route ordering](https://caddyserver.com/docs/caddyfile/directives/route).

**KO:** 같은 목적의 루프백 리스너입니다. 위처럼 관리자 CIDR을 바꾸세요. `remote_ip`는 직접 연결한 peer를 검사하므로 앞에 다른 프록시가 있으면 해당 peer가 의도한 접근 경계인지 확인해야 합니다. 신뢰하지 않는 전달 헤더를 관리자 식별로 쓰지 마세요. `route`는 순서를 유지합니다. Gemini matcher는 GET과 `/v1beta/models/*`를 모두 요구하므로 POST `:generateContent`/`:streamGenerateContent`와 정확한 GET `/v1beta/models`는 프록시됩니다. 실제 사이트 주소는 TLS 호스트명으로 바꾸세요. 근거는 위 Caddy 공식 문서입니다.

```caddyfile
http://127.0.0.1:8080 {
    @live path /v1/realtime* /v1/live*
    @rawManagementNonAdmin {
        path /v0/management/api-call /v8/management/requests/api-call
        not remote_ip 10.20.30.0/24
    }
    @alpha path /v1/alpha/search /backend-api/codex/alpha/search
    @responsesWS {
        method GET
        path /v1/responses /v1/responses/ /backend-api/codex/responses /backend-api/codex/responses/
    }
    @geminiDetail {
        method GET
        path /v1beta/models/*
    }
    @videoContent {
        method GET
        path_regexp video ^/openai/v1/videos/[^/]+/content/?$
    }
    route {
        respond @live 403
        respond @rawManagementNonAdmin 403
        respond @alpha 403
        respond @responsesWS 403
        respond @geminiDetail 403
        respond @videoContent 403
        reverse_proxy 127.0.0.1:8317 {
            flush_interval -1
        }
    }
}
```

**Verification status / 검증 상태 (2026-10-27):** Source/documentation checked only; neither proxy was run and no installed nginx/Caddy binary was available for syntax checks. Valid-key gateway denials, admin CIDR enforcement, POST/list/resource preservation and direct-CPA isolation are **unverified deployment requirements**, not harness passes. / 소스·공식 문서만 확인했습니다. 프록시 실행과 설치된 바이너리의 구문 검사는 하지 못했습니다. 유효 키 차단, 관리자 CIDR, POST·목록·리소스 유지, CPA 직접 접근 차단은 하네스 통과가 아닌 **미검증 배포 요구 사항**입니다.

**EN:** Validate configuration with your installed `nginx -t` or `caddy validate --config Caddyfile`. Verify blocked paths return the gateway's 403 even with valid credentials. Verify POST Responses remains usable, list routes remain filtered, management-key-only plugin APIs work, and clients cannot reach CPA directly. These blocks do not address malicious native code or post-AfterAuth payload rewrites; audit those separately. A host route change requires reviewing these restrictions again.

**KO:** 설치된 버전의 `nginx -t` 또는 `caddy validate --config Caddyfile`로 구문을 확인하세요. 유효한 키를 보내도 차단 경로가 게이트웨이 403을 반환하는지, POST Responses와 필터링된 목록, 관리 키 전용 플러그인 API가 동작하는지, 클라이언트의 CPA 직접 접근이 막혔는지 확인하세요. 악성 네이티브 코드나 AfterAuth 이후 payload 변경은 이 차단으로 해결되지 않으므로 별도 검토가 필요합니다. 호스트 라우트가 바뀌면 다시 검토하세요.

## State, backup and restore / 상태·백업·복원

**EN:** Use an absolute persistent `state-dir`, writable only by the intended service account/administrators. Containers need a persistent mount at that path. The default uses the CPA process user's home, not the administrator's shell home. Back up `state.json`, `state.json.bak`, and `history.jsonl` together while CPA/plugin writes are stopped. Do not run two CPA instances or overlapping plugin writers against one state directory; the store has no cross-process locking. Library replacement/uninstall does not erase this separate directory.

**KO:** 서비스 계정·관리자가 접근하는 쓰기 가능한 절대 `state-dir`을 사용하세요. 컨테이너는 해당 경로에 영속 볼륨이 필요합니다. 기본값은 관리자 셸이 아니라 CPA 프로세스 사용자의 홈입니다. CPA/플러그인 쓰기를 중지한 상태에서 `state.json`, `state.json.bak`, `history.jsonl`을 함께 백업하세요. 프로세스 간 잠금이 없으므로 같은 디렉터리를 두 CPA 인스턴스나 겹치는 플러그인 writer가 사용하면 안 됩니다. 라이브러리 교체·제거는 별도 상태 디렉터리를 지우지 않습니다.

**EN:** Writes use same-directory temporary files, fsync, rename, and directory sync; the previous valid policy is retained as the backup. History is capped at 1000 entries and compacted atomically. Startup tries primary, then backup; defaults are used only when neither policy file exists. If existing policy files are corrupt with no valid fallback, the plugin keeps management available to repair the policy but denies covered requests with `policy_unavailable`. Do not delete corrupt files to obtain permissive defaults: preserve evidence/backups, fix permissions, and save a known-good policy through the authenticated API. Restart-based file recovery should stop all writers first.

**KO:** 같은 디렉터리의 임시 파일, fsync, rename, 디렉터리 sync로 쓰며 이전 정상 정책을 백업에 남깁니다. 이력은 1000개로 제한하고 원자적으로 압축합니다. 시작 시 원본, 백업 순으로 시도하고 둘 다 없을 때만 기본값을 사용합니다. 기존 정책 파일이 손상되고 정상 사본이 없으면 관리 API로 복구할 수 있지만 지원 요청은 `policy_unavailable`로 거부합니다. 허용적인 기본값을 얻으려고 손상 파일을 지우지 말고 증거·백업을 보존한 뒤 권한을 고치고 인증 API로 정상 정책을 저장하세요. 파일을 통한 재시작 복구는 모든 writer를 중지하고 수행합니다.

Sources / 근거: [store](../internal/store/store.go), [config/application](../app.go), [management input](../internal/mgmt/input.go).

### Undo a historical change / 이력 변경 되돌리기

**EN:** History restoration undoes to the selected entry's **Before** value as a new revision. State scope restores the full policy; `scope:"alias_target"` restores that alias's previous target in the current policy and fails validation if the alias no longer exists. Revision never rolls backward and history is retained. This API has unit coverage; the real-CPA harness tested restart persistence rather than this restore operation.

**KO:** 이력 복원은 선택 항목의 **Before** 값으로 되돌리며 새 리비전을 만듭니다. state 범위는 전체 정책을, `scope:"alias_target"`은 현재 정책에서 해당 별칭의 이전 대상만 복원하며 별칭이 없으면 검증에 실패합니다. 리비전을 낮추거나 이력을 지우지 않습니다. 이 API는 단위 테스트 근거이며 실제 CPA 하네스는 이 복원 작업이 아닌 재시작 영속화를 확인했습니다.

```sh
# CPA_ORIGIN and CPA_MANAGEMENT_KEY are set privately by the operator.
DOCK_API="$CPA_ORIGIN/v0/management/plugins/cpa-model-dock"
curl --fail-with-body -sS "$DOCK_API/history?limit=20" \
  -H "Authorization: Bearer $CPA_MANAGEMENT_KEY" | jq .
# Review before/after, then set the chosen existing entry ID.
# entry_id=...
revision=$(curl --fail-with-body -sS "$DOCK_API/state" \
  -H "Authorization: Bearer $CPA_MANAGEMENT_KEY" | jq -er '.revision')
jq -n --arg entry_id "$entry_id" --argjson revision "$revision" \
  '{entry_id:$entry_id,revision:$revision,scope:"state"}' |
  curl --fail-with-body -sS -X POST "$DOCK_API/history/restore" \
    -H "Authorization: Bearer $CPA_MANAGEMENT_KEY" \
    -H 'Content-Type: application/json' --data-binary @-
```

**EN:** Restoration validates the proposed policy before saving. A 409 requires reviewing the newer state; a 422 requires repairing the policy (for example a historical alias that no longer exists). For an external saved policy file, use the README's POST-validate → GET-revision → PUT-state helper and include only the intended full policy. Neither restoring history nor saving JSON proves upstream routability.

**KO:** 복원도 저장 전에 정책을 검증합니다. 409이면 최신 상태를 검토하고, 422이면 더 이상 존재하지 않는 이력 별칭 등 정책 문제를 고쳐야 합니다. 외부 정책 파일에는 README의 POST 검증 → GET 리비전 → PUT 저장 함수를 사용하고 의도한 전체 정책만 포함하세요. 이력 복원이나 JSON 저장 자체가 업스트림 라우팅 가능성을 증명하지는 않습니다.

## Operational checks / 운영 확인

| Check / 확인 | What it establishes / 확인 범위 |
| --- | --- |
| Loaded/enabled plugin, version, unique highest priority / 로드·활성·버전·유일한 최고 우선순위 | Correct instance is selected; still not universal enforcement / 올바른 인스턴스 선택, 전체 집행 보증은 아님 |
| GET overview/state/catalog / 개요·상태·목록 | Saved policy, revision, refresh health, runtime credential evidence / 저장 정책·리비전·갱신·자격 증명 근거 |
| POST validate and PUT state / 검증·저장 | Schema/reference checks and persistence / 스키마·참조 검사와 영속화 |
| Explain / 정책 설명 | Policy decision only; no upstream call / 정책 판정만 확인, 업스트림 호출 없음 |
| Real inference with upstream recorder / 업스트림 기록을 갖춘 추론 | Routing/rewrite behavior for tested protocol and credentials / 테스트한 프로토콜·자격 증명의 라우팅·치환 |
| Negative inference plus no recorded upstream call / 거부 응답과 업스트림 호출 없음 | Actual tested denial, unlike an unrelated native 400/503 / 무관한 네이티브 400/503과 구분되는 실제 거부 |
| E2E_NO_PLUGIN=1 | Host baseline only; plugin scenarios skipped / 호스트 기준만 확인, 플러그인 시나리오 제외 |

**EN:** Preserve environment/version, configuration, PASS/FAIL/SKIP output and artifacts. The [final evidence](verification.md) records 29 PASS / 0 FAIL / 0 SKIP on real CPA v8.0.44 with a fake compatible upstream, including routing/rewrite, numeric upgrades and restart recovery. Earlier four-example validation was management-only. Headless Chrome verified all four tabs in EN/KO with strict CSP and no observed violations; management-center iframe login was not tested beyond resource loading. Live Codex OAuth forwarding, Responses WS continuation behavior and raw management bypasses are source-verified only; live provider credentials and proxy enforcement remain unverified.

**KO:** 환경·버전·설정·PASS/FAIL/SKIP 출력과 산출물을 보존하세요. [최종 근거](verification.md)는 실제 CPA v8.0.44와 가짜 호환 업스트림에서 29 PASS / 0 FAIL / 0 SKIP이며 라우팅·치환·숫자 버전 업그레이드·재시작 복구를 포함합니다. 이전 예제 네 개 검사는 관리 API만의 근거입니다. headless Chrome은 네 탭의 EN/KO와 엄격한 CSP, 관찰된 위반 없음을 확인했지만 관리 센터 iframe 로그인은 리소스 로딩 외에 미검증입니다. 실제 Codex OAuth 전달·Responses WS continuation·관리 원시 요청 우회는 소스만 확인했으며 실제 제공자 자격 증명과 프록시 집행은 미검증입니다.

**EN:** The harness observation rows confirm **no realtime/live guard denial with valid built-in keys** (native 503 `auth_not_found` both whitelist off/on) and **hidden Gemini detail metadata exposed with HTTP 200**. Block those paths at the mandatory gateway. WS continuation frames cannot be selected by path once upgraded; strict policy requires blocking the two Responses GET upgrade paths above. With the guard enabled, Alpha is denied whenever whitelist rules or alias target hiding require final-model authorization; published Dock aliases are always denied even with guard off. Exact native/prefix/raw allows are not sufficient mapping evidence. Both Alpha paths returned denial-executor 503 and zero upstream calls in the harness. Unrestricted default policy passes native Alpha through, but no Codex credentials were present to exercise forwarding. See [contract §6](design/contract.md#6-guard--uncovered-endpoints).

**KO:** 관찰 행에서 **유효한 기본 키로 realtime/live guard 거부가 없음**(whitelist off/on 모두 네이티브 503 `auth_not_found`)과 **Gemini 숨김 상세 메타데이터 HTTP 200 노출**을 확인했습니다. 필수 게이트웨이에서 차단하세요. WS 업그레이드 후 continuation 프레임은 경로로 선택할 수 없으므로 엄격한 정책은 위 Responses GET 두 경로를 차단해야 합니다. guard가 켜져 있고 허용 목록 규칙·별칭 대상 숨김이 최종 모델 승인을 요구하면 Alpha는 거부됩니다. 공개 Dock 별칭은 guard가 꺼져도 항상 거부되며 exact native/prefix/raw 허용만으로 매핑을 검증할 수 없습니다. 하네스에서 Alpha 두 경로 모두 거부 executor의 503과 업스트림 호출 0회였습니다. 제한 없는 기본 정책은 네이티브 Alpha를 통과시키지만 전달을 실행할 Codex 자격 증명은 없었습니다. [계약 §6](design/contract.md#6-guard--uncovered-endpoints)을 참고하세요.
