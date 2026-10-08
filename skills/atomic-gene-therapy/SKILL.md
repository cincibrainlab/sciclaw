---
name: atomic-gene-therapy
description: Run the fixed synthetic Genesis gene-therapy demonstration workflow through Gitea MCP. This is never clinical or regulatory guidance.
---

# Atomic Gene Therapy Demo

This workflow is synthetic demonstration policy only, scoped exactly to `genesis-demo-2026-10-13`. Never present it as clinical, patient-care, or regulatory guidance. Never display MCP or Gitea credentials.

Interact with Gitea only through `mcp-server-cli call TOOL_NAME '<json-arguments>'` using its configured Gitea MCP environment. Shell may launch the CLI and process its returned text in memory, but must not access Gitea through HTTP/API calls, `git` remotes, another client, or by treating remote repository paths as local filesystem paths. Do not stage remote content in temporary or local files.

## Natural-language activation

Use this skill for an unambiguous prose request about the synthetic Genesis demonstration, its fixed organization, labs, therapy onboarding, work items, evidence, attestations, status, or gates. Map the request to one of `setup`, `workspace`, `onboard`, `status`, `item`, `evidence`, `attest`, or `gate`.

Do not activate this skill for clinical, regulatory, patient-care, real-world gene-therapy, or ambiguous requests. Ask a focused question when the intended workflow, target, or required values are unclear.

## Tool invocation

- Make each Gitea operation as a separate `mcp-server-cli` call. For content changes, read through MCP, decode and validate the returned content in memory, then make a separate MCP write call using the SHA returned by that read. Never combine the read, transformation, and write into one shell command.
- Before each mutation in a therapy repository where `onboarding.yml` exists, read it and its SHA. Include the updated state in the same `create_or_update_file` call as the related file mutation. If a mutation uses a separate tool, immediately follow its successful result with a state update through `create_or_update_file`, using the latest SHA; do not proceed until state matches the mutation. Never record a failed mutation as successful. Organization, repository, and initial branch setup precede therapy state; after creating the initial branch, immediately create the onboarding tree with `onboarding.yml`.
- A sufficiently confident read-only request may invoke the appropriate read or reporting tool without confirmation, provided it does not change data.
- Invoke a modifying or destructive tool as soon as the requested workflow, target, and values are unambiguous. The unambiguous request authorizes the synthetic action.
- Never guess missing values, targets, or evidence. Ask only for the ambiguous or missing value before a modifying call.

## Native Discord Commands

Discord presents this workflow as one native `/atomic-gene-therapy` command with a `workflow` dropdown parameter:

- `setup`
- `workspace`
- `onboard`
- `status`
- `item`
- `evidence`
- `attest`
- `gate`

The optional `details` field accepts multiple semicolon-separated values. Discord autocomplete returns all workflow-specific templates in an empty field; type `;` after a selected value to append another suggestion. Ask for anything else the selected workflow needs. The submitted workflow and complete details authorize the requested mutation. For evidence paths, first read the selected file record and use its exact evidence type and attestation statement. Users MUST NOT be asked to provide JSON or MCP argument names.

The replacement server is generic Gitea MCP. Never call the removed Genesis tool names. This skill, not the MCP server, enforces every rule below.

## Fixed policy

- Phase directories have no owners. Each phase file has an `onboarding.yml` record with its `path`, `phase`, and `owner`; require its owner's username when the file is added and never use a placeholder. Initialize the file list empty.
- The only organization is `genesis-demo-2026-10-13`; the only labs/repos are private `smith-lab` and `jones-lab`.
- A therapy slug must be lowercase kebab-case: `^[a-z0-9]+(?:-[a-z0-9]+)*$`. Its branch is exactly `onboarding/<therapy-slug>` and targets `main`.
- Require all seven classifications before onboarding: route (`intravenous`, `intrathecal`, `intramuscular`, `local`, `other`); modality (`viral-vector`, `non-viral`, `cell-therapy`, `other`); ex vivo (`yes`, `no`, `unknown`); storage (`cryogenic`, `frozen`, `refrigerated`, `ambient`, `other`); funding (`commercial`, `government`, `sponsored-research`, `self-pay`, `other`); site REMS (`required`, `not-required`, `unknown`); pathway (`standard`, `expanded-access`, `clinical-trial`, `other`).
- Parse five complete, unambiguous dates to `YYYY-MM-DD`, retain their stated order as `P1`–`P5`, and ask only for an ambiguous or missing year.
- Store the complete synthetic state in `therapies/<therapy-slug>/onboarding.yml`: organization, lab, therapy, classification, `currentPhase`, onboarding PR metadata, `files: []`, evidence, attestations, evidence checks, and gate state. Each later file record contains its path, phase, and owner; do not prepopulate items or Markdown file references. `get_file_contents` returns base64 content; decode it before validating or modifying the JSON document.

## Onboarding structure

Before any AtomicQMS mutation, summarize the proposed synthetic therapy and five target dates. Do not create a partial tree.

```text
therapies/<therapy-slug>/
├── onboarding.yml
├── .approval-config.yml
├── README.md
├── 01-governance-compliance/
├── 02-financial-funding/
├── 03-product-handling-logistics/
├── 04-clinical-protocol-safety/
├── 05-patient-identification-screening/
├── evidence/
└── attestations/
```

Create only `.gitkeep` in each numbered phase directory, plus `evidence/.gitkeep` and `attestations/.gitkeep`. Do not create phase work-item Markdown files or file records during onboarding. The onboarding branch and pull request must contain the entire tree above. Do not write directly to `main`; never merge the pull request.

1. Set up the demo division: call `get_user_orgs`, then call `create_organization` with `{"username":"genesis-demo-2026-10-13","visibility":"private"}` only when absent.
2. Create a PI workspace: call `list_org_repos` with `{"org":"genesis-demo-2026-10-13"}`, then call `create_repo` with `{"organization":"genesis-demo-2026-10-13","name":"<lab>","private":true,"auto_init":true}` only when the approved lab (`smith-lab` or `jones-lab`) is absent.
3. Create therapy onboarding only after verifying the private workspace exists. If an onboarding PR already exists, require its base to be `main` and report it without creating a duplicate. Otherwise, call `create_branch` with `owner`, `repo`, `branch:"onboarding/<therapy-slug>"`, and `old_branch:"main"`; then call `create_or_update_file` once with `owner`, `repo`, `branch_name`, `message`, and `files:[{"path":"...","content":"..."}]` to create `onboarding.yml`, README, approval config, and `.gitkeep` in each of the five phase, evidence, and attestation directories. Initialize `files: []` in `onboarding.yml`. Finally call `pull_request_write` with `{"method":"create","owner":"genesis-demo-2026-10-13","repo":"<lab>","head":"onboarding/<therapy-slug>","base":"main","title":"Synthetic onboarding: <therapy-name>"}`; immediately update `onboarding.yml` with the returned PR metadata using the current file SHA.
4. Before any direct mutation, call `pull_request_read` with `{"method":"get","owner":"genesis-demo-2026-10-13","repo":"<lab>","pull_number":<onboarding-pr-number>}` and require that the recorded onboarding branch is merged to `main`. Read `onboarding.yml` from `main`, validate its organization, lab, slug, and branch, apply the corresponding state change, and write it through `create_or_update_file` with the SHA returned by the read in `files:[{"path":"...","content":"...","sha":"..."}]`; do not overwrite without that SHA.
5. When adding a phase file, add its `path`, `phase`, and `owner` record to `onboarding.yml`; file owners are required. A file update changes only that file's `completed` flag. Evidence must name that file's exact evidence type, explicitly be synthetic, and contain exactly one value: synthetic text of at most 10,000 characters or a credential-free `https:` URL. An attestation must exactly match the file's recorded statement. Record all attestations and evidence checks with `unverifiedDemoMetadata:true` and a `confirmedBy` value. Each successful mutation updates `onboarding.yml` before another mutation or response.
6. Advance only the current sequential gate when it has at least one file and every file in it is completed and has matching evidence, an exact attestation, and a matching `true` evidence check. Update only the gate state and `currentPhase` in `onboarding.yml`; there are no phase files to move. Mark the gate advanced and set the next phase, or `complete` after `P5`.
7. For status, use `list_org_repos`, `get_file_contents` (`ref:"main"`) to read `onboarding.yml`, and `list_pull_requests`. Do not enumerate phase directories. Do not combine the organization and lab in one identifier.

The agent, not the user, converts the user's request to compact JSON for the MCP call. For ordinary-sized arguments:

```sh
mcp-server-cli call TOOL_NAME '<json-arguments>'
```

For a fixed sequence where an exact value from one MCP result feeds a later call, use one batch so intermediate results do not enter model context:

```sh
mcp-server-cli batch <<'JSON'
{"steps":[{"id":"first","tool":"FIRST_TOOL","arguments":{}},{"id":"second","tool":"SECOND_TOOL","arguments":{"value":{"$ref":"first","pointer":"/value/result"}}}],"return":"second"}
JSON
```

`$ref` may target only an earlier step. `pointer` is a JSON Pointer into that result; when a result is one text item containing valid JSON, its parsed value is available under `/value`. The CLI executes steps sequentially through one MCP connection, stops on the first error, and prints only the selected `return` result. Keep using `call` for ordinary one-off operations. For large arguments such as base64 file content, use a one-step batch and pass its JSON through stdin (for example, `mcp-server-cli batch < request.json`) to avoid OS command-line size limits. Never ask the user to provide this JSON.

Do not invent an organization, owner, repository, branch, phase, item, classification value, or evidence type. Report errors faithfully; onboarding must be merged before direct mutations and this skill never merges it.
