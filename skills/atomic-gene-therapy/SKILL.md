---
name: atomic-gene-therapy
description: Run the fixed synthetic Genesis gene-therapy demonstration workflow through Gitea MCP. This is never clinical or regulatory guidance.
---

# Atomic Gene Therapy Demo

This workflow is synthetic demonstration policy only, scoped exactly to `genesis-demo-2026-10-13`. Never present it as clinical, patient-care, or regulatory guidance. Never display MCP or Gitea credentials.

Use `mcp-server-cli` with its configured Gitea MCP environment. Do not use shell or HTTP alternatives.

## Natural-language activation

Use this skill for an unambiguous prose request about the synthetic Genesis demonstration, its fixed organization, labs, therapy onboarding, work items, evidence, attestations, status, or gates. Map the request to one of `setup`, `workspace`, `onboard`, `status`, `item`, `evidence`, `attest`, or `gate`.

Do not activate this skill for clinical, regulatory, patient-care, real-world gene-therapy, or ambiguous requests. Ask a focused question when the intended workflow, target, or required values are unclear.

## Tool invocation

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

The optional `details` field accepts multiple semicolon-separated values. Discord autocomplete returns all workflow-specific templates in an empty field; type `;` after a selected value to append another suggestion. Ask for anything else the selected workflow needs. The submitted workflow and complete details authorize the requested mutation. For evidence paths, first read the selected fixed item and use its exact evidence type and attestation statement. Users MUST NOT be asked to provide JSON or MCP argument names.

The replacement server is generic Gitea MCP. Never call the removed Genesis tool names. This skill, not the MCP server, enforces every rule below.

## Fixed policy

- The only organization is `genesis-demo-2026-10-13`; the only labs/repos are private `smith-lab` and `jones-lab`.
- A therapy slug must be lowercase kebab-case: `^[a-z0-9]+(?:-[a-z0-9]+)*$`. Its branch is exactly `onboarding/<therapy-slug>` and targets `main`.
- Require all seven classifications before onboarding: route (`intravenous`, `intrathecal`, `intramuscular`, `local`, `other`); modality (`viral-vector`, `non-viral`, `cell-therapy`, `other`); ex vivo (`yes`, `no`, `unknown`); storage (`cryogenic`, `frozen`, `refrigerated`, `ambient`, `other`); funding (`commercial`, `government`, `sponsored-research`, `self-pay`, `other`); site REMS (`required`, `not-required`, `unknown`); pathway (`standard`, `expanded-access`, `clinical-trial`, `other`).
- Parse five complete, unambiguous dates to `YYYY-MM-DD`, retain their stated order as `P1`–`P5`, and ask only for an ambiguous or missing year.
- The fixed work items are `P1-001` governance/IBC approval note; `P2-001` funding/revenue-cycle payer agreement; `P3-001` logistics/pharmacy storage validation; `P4-001` safety/nursing training log; and `P5-001` screening/labs screening record. Their matching attestations are respectively: “Sample governance approval is documented.”, “Sample funding readiness is documented.”, “Sample storage readiness is documented.”, “Sample protocol and training readiness are documented.”, and “Sample screening readiness are documented.”
- Store the complete synthetic state in `therapies/<therapy-slug>/onboarding.yml`: organization, lab, therapy, classification, `currentPhase`, onboarding PR metadata, the five items, evidence, attestations, evidence checks, and gate state. `get_file_contents` returns base64 content; decode it before validating or modifying the JSON document.

## Onboarding structure

Before any AtomicQMS mutation, summarize the proposed synthetic therapy, five target dates, and work-item IDs. Do not create a partial tree.

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

Create one synthetic Markdown work-item file in each numbered phase directory, plus `evidence/.gitkeep` and `attestations/.gitkeep`. The onboarding branch and pull request must contain the entire tree above. Do not write directly to `main`; never merge the pull request.

1. Set up the demo division: call `get_user_orgs`, then call `create_organization` with `{"username":"genesis-demo-2026-10-13","visibility":"private"}` only when absent.
2. Create a PI workspace: call `list_org_repos` with `{"org":"genesis-demo-2026-10-13"}`, then call `create_repo` with `{"organization":"genesis-demo-2026-10-13","name":"<lab>","private":true,"auto_init":true}` only when the approved lab (`smith-lab` or `jones-lab`) is absent.
3. Create therapy onboarding only after verifying the private workspace exists. If an onboarding PR already exists, require its base to be `main` and report it without creating a duplicate. Otherwise, call `create_branch` with `owner`, `repo`, `branch:"onboarding/<therapy-slug>"`, and `old_branch:"main"`; then call `create_or_update_file` once with `owner`, `repo`, `branch_name`, `message`, and `files:[{"path":"...","content":"..."}]` to create the state, README, approval config, five phase files, evidence directory marker, and attestation directory marker. Call `label_read`, create only missing `therapy:<slug>`, `phase:<phase>`, `office:<owner>`, and `state:open` labels through `label_write` (`method:"create_repo_label"`, `color:"#5b8c85"`), then create the five labeled issues through `issue_write` (`method:"create"`). Finally call `pull_request_write` with `{"method":"create","owner":"genesis-demo-2026-10-13","repo":"<lab>","head":"onboarding/<therapy-slug>","base":"main","title":"Synthetic onboarding: <therapy-name>"}`, then update the branch state file with the returned PR metadata and its current SHA.
4. Before any direct mutation, call `pull_request_read` with `{"method":"get","owner":"genesis-demo-2026-10-13","repo":"<lab>","pull_number":<onboarding-pr-number>}` and require that the recorded onboarding branch is merged to `main`. Read the state from `main`, validate its organization, lab, slug, and branch, then update it through `create_or_update_file` with its returned SHA in `files:[{"path":"...","content":"...","sha":"..."}]`; do not overwrite without that SHA.
5. An item update changes only a fixed item’s `completed` flag. Evidence must name that item’s exact evidence type, explicitly be synthetic, and contain exactly one value: synthetic text of at most 10,000 characters or a credential-free `https:` URL. An attestation must exactly match the fixed statement. Record all attestations and evidence checks with `unverifiedDemoMetadata:true` and a `confirmedBy` value.
6. Advance only the current sequential gate when every item in it is completed and has matching evidence, an exact attestation, and a matching `true` evidence check. Update the state and move that phase’s work-item files to the next fixed phase folder in one `create_or_update_file` call. A rename entry is `{"path":"<destination>","from_path":"<source>","sha":"<source-sha>"}`. Mark the gate advanced and set the next phase, or `complete` after `P5`.
7. For status, use `list_org_repos`, `get_file_contents` (`ref:"main"`), `list_issues`, and `list_pull_requests`. Do not combine the organization and lab in one identifier.

The agent, not the user, converts the user's request to compact JSON for the MCP call:

```sh
mcp-server-cli call TOOL_NAME '<json-arguments>'
```

Do not invent an organization, owner, repository, branch, phase, item, classification value, or evidence type. Report errors faithfully; onboarding must be merged before direct mutations and this skill never merges it.
