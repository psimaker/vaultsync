#!/usr/bin/env ruby
# frozen_string_literal: true

require "json"
require "yaml"

ROOT = File.expand_path("../..", __dir__)
WORKFLOW_PATH = File.join(ROOT, ".github/workflows/docker.yml")
CI_PATH = File.join(ROOT, ".github/workflows/ci.yml")
SECURITY_PATH = File.join(ROOT, ".github/workflows/security.yml")
RELEASE_SPEC_PATH = File.join(ROOT, "notify/release.json")
DOCKERFILE_PATH = File.join(ROOT, "notify/Dockerfile")
INSTALL_PATH = File.join(ROOT, "notify/scripts/install.sh")
COMPOSE_PATH = File.join(ROOT, "notify/docker-compose.yml")
PINNED_ACTION = /\A[^@]+@[0-9a-f]{40}\z/
WRITE_JOBS = %w[publish-image attest-binaries release-binaries rollout-verify finalize-release].freeze
NORMAL_ONLY_JOBS = %w[publish-image attest-binaries].freeze
RECOVERY_ONLY_JOBS = %w[recover-image].freeze
DUAL_PATH_JOBS = %w[release-binaries rollout-verify finalize-release verify-published].freeze
AGGREGATE_JOBS = %w[image-ready binary-attestation-ready].freeze
SEQUENCED_PUBLICATION_JOBS = %w[release-binaries rollout-verify finalize-release].freeze
DISPATCH_JOBS = (NORMAL_ONLY_JOBS + RECOVERY_ONLY_JOBS + DUAL_PATH_JOBS + AGGREGATE_JOBS).freeze
MANUAL_JOBS = (DISPATCH_JOBS + ["publish-gate"]).freeze
COMMON_GATE_FRAGMENTS = [
  "github.event_name == 'workflow_dispatch'",
  "inputs.confirmation == 'PUBLISH_NOTIFY_RELEASE'",
  "github.actor == github.repository_owner",
  "github.triggering_actor == github.repository_owner"
].freeze
NORMAL_GATE_FRAGMENTS = [
  "github.ref_type == 'tag'",
  "inputs.release_tag == github.ref_name",
  "inputs.recovery_run_id == ''"
].freeze
RECOVERY_GATE_FRAGMENTS = [
  "github.ref == 'refs/heads/main'",
  "inputs.recovery_run_id != ''"
].freeze
SOURCE_CHECKOUT_JOBS = %w[
  publish-image recover-image attest-binaries release-binaries rollout-verify
  finalize-release verify-published
].freeze
EXPECTED_BINARIES = %w[
  vaultsync-notify_linux_amd64
  vaultsync-notify_linux_arm64
  vaultsync-notify_darwin_amd64
  vaultsync-notify_darwin_arm64
  vaultsync-notify_windows_amd64.exe
].freeze
EXPECTED_ASSETS = (EXPECTED_BINARIES + %w[
  SHA256SUMS
  SBOM.spdx.json
  IMAGE-DIGESTS
  RELEASE-MANIFEST.json
  ROLLOUT-EVIDENCE.txt
]).freeze
PUBLICATION_WRITE_KINDS = %w[
  image image-provenance image-sbom binary-provenance binary-sbom
  release-create release-asset rollout-evidence release-finalization
].freeze

def fail_policy(message)
  warn "notify publish safety policy: #{message}"
  exit 1
end

def assert_policy(condition, message)
  fail_policy(message) unless condition
end

def check_duplicate_yaml_keys(node, path = "root")
  case node
  when Psych::Nodes::Document, Psych::Nodes::Stream
    node.children.each { |child| check_duplicate_yaml_keys(child, path) }
  when Psych::Nodes::Mapping
    seen = {}
    node.children.each_slice(2) do |key, value|
      key_name = key.respond_to?(:value) ? key.value : key.to_s
      fail_policy("duplicate YAML key #{path}.#{key_name}") if seen[key_name]

      seen[key_name] = true
      check_duplicate_yaml_keys(value, "#{path}.#{key_name}")
    end
  when Psych::Nodes::Sequence
    node.children.each_with_index { |child, index| check_duplicate_yaml_keys(child, "#{path}[#{index}]") }
  end
end

def load_workflow(path)
  check_duplicate_yaml_keys(Psych.parse_stream(File.read(path)), File.basename(path))
  YAML.safe_load(File.read(path), permitted_classes: [], permitted_symbols: [], aliases: false)
rescue Psych::Exception => e
  fail_policy("#{path} is not valid YAML: #{e.message}")
end

def triggers(workflow)
  workflow.fetch("on") { workflow.fetch(true) }
end

def steps(job)
  job.fetch("steps", [])
end

def flattened_step_text(job)
  steps(job).flat_map do |step|
    [step["uses"], step["run"], step.fetch("with", {}).values]
  end.flatten.compact.join("\n")
end

def publication_allowed?(event:, ref_type:, ref_name:, release_tag:, recovery_run_id:, confirmation:, actor:,
                         triggering_actor:, owner:)
  ref_allowed = (ref_type == "tag" && ref_name == release_tag && recovery_run_id.empty?) ||
                (ref_type == "branch" && ref_name == "main" && recovery_run_id.match?(/\A[1-9][0-9]*\z/))
  event == "workflow_dispatch" &&
    ref_allowed &&
    confirmation == "PUBLISH_NOTIFY_RELEASE" &&
    actor == owner &&
    triggering_actor == owner &&
    release_tag.match?(/\Anotify-v(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\z/)
end

def resume_action(existing_digest, expected_digest, mutable:)
  return mutable ? :upload : :abort if existing_digest.nil?
  return :reuse if existing_digest == expected_digest

  :abort
end

workflow = load_workflow(WORKFLOW_PATH)
ci = load_workflow(CI_PATH)
security = load_workflow(SECURITY_PATH)
spec = JSON.parse(File.read(RELEASE_SPEC_PATH))
workflow_triggers = triggers(workflow)
jobs = workflow.fetch("jobs")

assert_policy(workflow.fetch("permissions") == { "contents" => "read" },
              "top-level permissions must be exactly contents: read")
assert_policy(workflow_triggers.keys.sort == %w[pull_request push workflow_dispatch],
              "only pull_request, push, and workflow_dispatch triggers are allowed")
assert_policy(workflow_triggers.fetch("push").fetch("branches") == ["main"],
              "push validation must target main only")
assert_policy((workflow_triggers.fetch("push").keys & %w[tags tags-ignore]).empty?,
              "push must never publish or trigger from tags")
assert_policy(workflow_triggers.fetch("pull_request").fetch("branches") == ["main"],
              "pull-request validation must target main")
guarded_paths = %w[
  notify/**
  .github/workflows/docker.yml
  .github/workflows/ci.yml
  .github/workflows/security.yml
  .github/scripts/notify-publish-safety.rb
]
%w[pull_request push].each do |event|
  assert_policy(workflow_triggers.fetch(event).fetch("paths").sort == guarded_paths.sort,
                "#{event} must validate every publish-policy input")
end

dispatch_inputs = workflow_triggers.fetch("workflow_dispatch").fetch("inputs")
%w[release_tag confirmation].each do |input|
  definition = dispatch_inputs.fetch(input)
  assert_policy(definition["required"] == true && definition["type"] == "string",
                "workflow_dispatch input #{input} must be a required string")
end
recovery_input = dispatch_inputs.fetch("recovery_run_id")
assert_policy(recovery_input["required"] == false && recovery_input["type"] == "string",
              "workflow_dispatch recovery_run_id must be an optional string")
concurrency_group = workflow.fetch("concurrency").fetch("group")
%w[inputs.release_tag github.actor github.triggering_actor inputs.confirmation github.run_id].each do |fragment|
  assert_policy(concurrency_group.include?(fragment),
                "publication concurrency group is missing #{fragment}")
end
assert_policy(workflow.fetch("concurrency").fetch("cancel-in-progress") ==
                "${{ github.event_name != 'workflow_dispatch' }}",
              "manual publications must queue without canceling an active run")

required_jobs = %w[
  publish-safety-policy notify-guard build-without-push publish-gate publish-image
  recover-image image-ready attest-binaries binary-attestation-ready release-binaries
  rollout-verify finalize-release verify-published
]
assert_policy((required_jobs - jobs.keys).empty?, "required publication-safety jobs are missing")
assert_policy(jobs.fetch("build-without-push").fetch("if") == "github.event_name != 'workflow_dispatch'",
              "the no-push build must be the only image build on normal events")
assert_policy(jobs.fetch("publish-gate").fetch("if") == "github.event_name == 'workflow_dispatch'",
              "the read-only publish gate must run only for manual dispatch")
assert_policy(jobs.fetch("publish-gate").fetch("permissions") == {
                "actions" => "read",
                "contents" => "read"
              }, "publish-gate must receive only run-history and repository read permissions")
source_security_steps = steps(jobs.fetch("publish-gate")).select do |step|
  step.fetch("uses", "").start_with?("aquasecurity/trivy-action@") &&
    step.fetch("with", {}).fetch("scan-ref", "") == "notify"
end
assert_policy(source_security_steps.length == 1 &&
              source_security_steps.first.fetch("with").fetch("scanners") == "vuln,secret" &&
              source_security_steps.first.fetch("with").fetch("exit-code") == "1",
              "exact helper source security scan must complete before publication jobs")

write_jobs = jobs.select do |_name, job|
  job.fetch("permissions", {}).value?("write")
end.keys.sort
assert_policy(write_jobs == WRITE_JOBS.sort,
              "only normal publication, draft staging, draft rollout, and finalization jobs may receive write permissions")
assert_policy(jobs.fetch("publish-image").fetch("permissions") == {
                "contents" => "read",
                "packages" => "write",
                "id-token" => "write",
                "attestations" => "write"
              }, "publish-image permissions changed")
assert_policy(jobs.fetch("recover-image").fetch("permissions") == {
                "contents" => "read",
                "packages" => "read"
              }, "recover-image must remain registry-read-only")
assert_policy(jobs.fetch("attest-binaries").fetch("permissions") == {
                "contents" => "read",
                "id-token" => "write",
                "attestations" => "write"
              }, "attest-binaries permissions changed")
assert_policy(jobs.fetch("release-binaries").fetch("permissions") == { "contents" => "write" },
              "release-binaries must receive only draft-asset write permission")
assert_policy(jobs.fetch("rollout-verify").fetch("permissions") == {
                "contents" => "write",
                "packages" => "read"
              }, "rollout-verify must receive only draft visibility and registry read permissions")
assert_policy(jobs.fetch("finalize-release").fetch("permissions") == { "contents" => "write" },
              "finalize-release permissions changed")
%w[image-ready binary-attestation-ready].each do |name|
  assert_policy(jobs.fetch(name).fetch("permissions") == { "contents" => "read" },
                "#{name} must remain read-only")
end

jobs.each do |name, job|
  steps(job).each do |step|
    assert_policy(!step.fetch("run", "").include?('${{'),
                  "#{name} interpolates a GitHub expression directly into a shell script")

    action = step["uses"]
    next unless action

    assert_policy(action.match?(PINNED_ACTION), "#{name} uses a non-immutable action ref: #{action}")
    next unless action.start_with?("actions/checkout@")

    assert_policy(step.fetch("with", {})["persist-credentials"] == false,
                  "#{name} checkout must set persist-credentials: false")
  end
end

SOURCE_CHECKOUT_JOBS.each do |name|
  checkout = steps(jobs.fetch(name)).find do |step|
    step.fetch("uses", "").start_with?("actions/checkout@")
  end
  assert_policy(checkout&.dig("with", "ref") ==
                "${{ needs.publish-gate.outputs.release_sha }}",
                "#{name} must check out the immutable source-tag commit")
end

security.fetch("jobs").each do |name, job|
  steps(job).each do |step|
    action = step["uses"]
    next unless action

    assert_policy(action.match?(PINNED_ACTION), "security/#{name} uses a non-immutable action ref: #{action}")
    next unless action.start_with?("actions/checkout@")

    assert_policy(step.fetch("with", {})["persist-credentials"] == false,
                  "security/#{name} checkout must set persist-credentials: false")
  end
end

login_jobs = jobs.select { |_name, job| flattened_step_text(job).include?("docker/login-action@") }.keys
assert_policy(login_jobs == %w[publish-image recover-image verify-published],
              "registry login must exist only in image publication and read-only verification")
assert_policy(jobs.fetch("recover-image").fetch("permissions").fetch("packages") == "read",
              "recovery image verifier registry permission must remain read-only")
assert_policy(jobs.fetch("verify-published").fetch("permissions").fetch("packages") == "read",
              "public verifier registry permission must remain read-only")
release_mutation_jobs = jobs.select do |_name, job|
  flattened_step_text(job).match?(/\bgh release (?:create|upload|edit|delete)\b/)
end.keys.sort
assert_policy(release_mutation_jobs == %w[finalize-release release-binaries],
              "release mutation must stay in staging and finalization")

normal_text = jobs.reject { |name, _job| MANUAL_JOBS.include?(name) }
                  .values.map { |job| flattened_step_text(job) }.join("\n")
[
  "docker/login-action@",
  "actions/upload-artifact@",
  "docker push",
  "gh release ",
  "secrets.GITHUB_TOKEN"
].each do |forbidden|
  assert_policy(!normal_text.include?(forbidden), "normal validation contains privileged operation #{forbidden}")
end

workflow_text = File.read(WORKFLOW_PATH)
assert_policy(!workflow_text.include?("--clobber"), "release assets must never be overwritten")
assert_policy(!workflow_text.include?("gh release delete"), "publication must never delete a release")
assert_policy(!workflow_text.include?("immutable-releases"),
              "publication must not mutate repository immutable-release settings")
assert_policy(!workflow_text.match?(/ghcr\.io\/psimaker\/vaultsync-notify:latest/),
              "publication must not read or move the helper latest tag")
assert_policy(!workflow_text.include?("type=raw,value=latest"), "image metadata must not create latest")
assert_policy(workflow_text.include?('http_status" = 404') &&
              workflow_text.include?("refusing to treat the version tag as absent"),
              "registry lookup errors must not be interpreted as an absent version tag")
assert_policy(workflow_text.include?('test "$release_is_draft" = true') &&
              workflow_text.include?('test "${expected_names[*]}" = "${actual_names[*]}"'),
              "public retries must be read-only and finalization must require the exact asset set")
assert_policy(workflow_text.include?('--source-digest "$RELEASE_SHA"') &&
              workflow_text.include?('--source-ref "refs/tags/${RELEASE_TAG}"') &&
              workflow_text.include?('--signer-workflow "${GITHUB_REPOSITORY}/.github/workflows/docker.yml"'),
              "attestation verification must bind source commit, tag ref, and signer workflow")

build_steps = jobs.flat_map do |name, job|
  steps(job).map do |step|
    [name, step] if step.fetch("uses", "").start_with?("docker/build-push-action@")
  end.compact
end
assert_policy(build_steps.length == 2, "expected exactly one validation build and one publication build")
validation_build = build_steps.assoc("build-without-push")&.last
publication_build = build_steps.assoc("publish-image")&.last
assert_policy(validation_build&.fetch("with", {})&.fetch("push") == false,
              "normal image build must set push: false")
assert_policy(publication_build&.fetch("with", {})&.fetch("push") == true,
              "owner publication image build must set push: true")
assert_policy(publication_build&.fetch("with", {})&.fetch("platforms") == "linux/amd64,linux/arm64",
              "published image platform set changed")

public_write_guard = "needs.publish-gate.outputs.release_is_public != 'true'"
action_write_steps = {
  "publish-image" => [
    "Build and publish the absent release image",
    "Attest image provenance",
    "Attest image SBOM"
  ],
  "attest-binaries" => [
    "Attest binary provenance",
    "Attest binary SBOM"
  ]
}
action_write_steps.each do |job_name, step_names|
  step_names.each do |step_name|
    step = steps(jobs.fetch(job_name)).find { |candidate| candidate["name"] == step_name }
    condition = step&.fetch("if", "") || ""
    assert_policy(step && condition.include?(public_write_guard),
                  "#{job_name}/#{step_name} remains reachable for a public release")
  end
end

public_integrity_checks = {
  "publish-image" => [
    "Detect an existing exact release image",
    "Check for existing repository provenance",
    "Check for existing repository SBOM attestation"
  ],
  "attest-binaries" => [
    "Check for existing exact binary provenance",
    "Check for existing exact binary SBOM attestation"
  ]
}
public_integrity_checks.each do |job_name, step_names|
  step_names.each do |step_name|
    step = steps(jobs.fetch(job_name)).find { |candidate| candidate["name"] == step_name }
    assert_policy(step &&
                  step.fetch("env", {})["RELEASE_IS_PUBLIC"] ==
                    "${{ needs.publish-gate.outputs.release_is_public }}" &&
                  step.fetch("run", "").include?('$RELEASE_IS_PUBLIC') &&
                  step.fetch("run", "").include?("exit 1"),
                  "#{job_name}/#{step_name} must fail closed on missing immutable material")
  end
end

{
  "Verify exact binary provenance" => "--source-digest",
  "Verify exact binary SBOM attestation" => "--predicate-type"
}.each do |step_name, required_fragment|
  step = steps(jobs.fetch("release-binaries")).find { |candidate| candidate["name"] == step_name }
  assert_policy(step && step.fetch("run", "").include?(required_fragment) &&
                step.fetch("run", "").include?("exit 1") &&
                !step.fetch("run", "").include?("actions/attest"),
                "release-binaries/#{step_name} must verify existing tag-bound attestations read-only")
end

recovery_text = flattened_step_text(jobs.fetch("recover-image"))
assert_policy(recovery_text.include?("gh attestation verify") &&
              !recovery_text.include?("actions/attest@") &&
              !recovery_text.include?("docker/build-push-action@"),
              "image recovery must only verify the already-published image and attestations")

[
  "Normalize deterministic attestation SBOM metadata",
  "Normalize deterministic SBOM metadata"
].each do |step_name|
  job_name = step_name.include?("attestation") ? "attest-binaries" : "release-binaries"
  step_text = steps(jobs.fetch(job_name)).find { |step| step["name"] == step_name }&.fetch("run", "") || ""
  assert_policy(step_text.include?("creationInfo.created") &&
                step_text.include?("documentNamespace") &&
                step_text.include?("walk(") &&
                step_text.include?("annotationDate") &&
                step_text.include?("1970-01-01T00:00:00Z"),
                "#{job_name}/#{step_name} must normalize every runtime timestamp")
end

adopt_sbom = steps(jobs.fetch("release-binaries")).find do |step|
  step["name"] == "Verify and adopt the immutable staged SBOM in recovery"
end
adopt_text = adopt_sbom&.fetch("run", "") || ""
assert_policy(adopt_sbom &&
              adopt_sbom.fetch("if", "").include?("needs.publish-gate.outputs.recovery_mode == 'true'") &&
              adopt_text.include?("gh api graphql") &&
              adopt_text.include?("databaseId") &&
              adopt_text.include?('releases/${release_id}') &&
              adopt_text.include?('test "$matches" = 1') &&
              adopt_text.include?('releases/assets/${asset_id}') &&
              adopt_text.include?("Accept: application/octet-stream") &&
              adopt_text.include?('^sha256:[0-9a-f]{64}$') &&
              adopt_text.include?("canonical_filter") &&
              adopt_text.include?("annotationDate") &&
              adopt_text.include?("cmp ") &&
              adopt_text.include?('mv "$staged" "$generated"') &&
              !adopt_text.match?(/gh release (?:upload|edit|delete)/),
              "recovery must adopt an existing SBOM only after exact API-digest and canonical-content verification")

stage_release = steps(jobs.fetch("release-binaries")).find do |step|
  step["name"] == "Create or verify the exact draft release and assets"
end
assert_policy(stage_release &&
              stage_release.fetch("env", {})["RELEASE_IS_PUBLIC"] ==
                "${{ needs.publish-gate.outputs.release_is_public }}" &&
              stage_release.fetch("env", {})["RECOVERY_MODE"] ==
                "${{ needs.publish-gate.outputs.recovery_mode }}" &&
              stage_release.fetch("run", "").include?('test "$RELEASE_IS_PUBLIC" != true') &&
              stage_release.fetch("run", "").include?('test "$RECOVERY_MODE" != true') &&
              stage_release.fetch("run", "").include?('test "$release_is_draft" = true') &&
              stage_release.fetch("run", "").include?("gh api graphql") &&
              stage_release.fetch("run", "").include?("databaseId") &&
              stage_release.fetch("run", "").include?('releases/${release_id}') &&
              stage_release.fetch("run", "").include?("|| return 1") &&
              !stage_release.fetch("run", "").include?("releases/tags"),
              "release creation and asset upload must be unreachable for a public release")

finalize_step = steps(jobs.fetch("finalize-release")).find do |step|
  step["name"] == "Revalidate the tag and publish the complete release once"
end
finalize_text = finalize_step&.fetch("run", "") || ""
public_branch = finalize_text.index('if [ "$release_is_draft" = false ]; then')
public_exit = public_branch && finalize_text.index("exit 0", public_branch)
release_edit = finalize_text.index('gh release edit "$RELEASE_TAG"')
assert_policy(finalize_text.include?('test "$release_is_draft" = true') &&
              finalize_text.include?('test "$body" = "$(cat /tmp/release-notes.md)"') &&
              finalize_text.include?("gh api graphql") &&
              finalize_text.include?("databaseId") &&
              finalize_text.include?('releases/${release_id}') &&
              finalize_text.include?('releases/assets/${evidence_id}') &&
              finalize_text.include?('Accept: application/octet-stream') &&
              !finalize_text.include?('gh release download "$RELEASE_TAG"') &&
              finalize_text.include?("|| return 1") &&
              public_branch && public_exit && release_edit && public_exit < release_edit,
              "rollout evidence and release finalization must be read-only once public")

DISPATCH_JOBS.each do |name|
  job = jobs.fetch(name)
  condition = job.fetch("if")
  COMMON_GATE_FRAGMENTS.each do |fragment|
    assert_policy(condition.include?(fragment), "#{name} is missing gate condition #{fragment}")
  end
  assert_policy((%w[publish-safety-policy notify-guard publish-gate] - job.fetch("needs")).empty?,
                "#{name} must depend on policy, tests, and the owner gate")
end

NORMAL_ONLY_JOBS.each do |name|
  condition = jobs.fetch(name).fetch("if")
  NORMAL_GATE_FRAGMENTS.each do |fragment|
    assert_policy(condition.include?(fragment), "#{name} is missing normal tag gate #{fragment}")
  end
  RECOVERY_GATE_FRAGMENTS.each do |fragment|
    assert_policy(!condition.include?(fragment), "#{name} must be unreachable from main-ref recovery")
  end
end

RECOVERY_ONLY_JOBS.each do |name|
  condition = jobs.fetch(name).fetch("if")
  RECOVERY_GATE_FRAGMENTS.each do |fragment|
    assert_policy(condition.include?(fragment), "#{name} is missing recovery gate #{fragment}")
  end
  NORMAL_GATE_FRAGMENTS.each do |fragment|
    assert_policy(!condition.include?(fragment), "#{name} must be unreachable from normal tag publication")
  end
end

DUAL_PATH_JOBS.each do |name|
  condition = jobs.fetch(name).fetch("if")
  assert_policy(condition.include?("always()"),
                "#{name} must evaluate explicit predecessor state after a skipped alternate path")
  (NORMAL_GATE_FRAGMENTS + RECOVERY_GATE_FRAGMENTS).each do |fragment|
    assert_policy(condition.include?(fragment), "#{name} is missing dual-path gate #{fragment}")
  end
end

SEQUENCED_PUBLICATION_JOBS.each do |name|
  condition = jobs.fetch(name).fetch("if")
  jobs.fetch(name).fetch("needs").each do |predecessor|
    result_gate = "needs.#{predecessor}.result == 'success'"
    assert_policy(condition.include?(result_gate),
                  "#{name} must fail closed unless #{predecessor} succeeded")
  end
end

verify_predecessors = {
  "PUBLISH_SAFETY_RESULT" => "${{ needs.publish-safety-policy.result }}",
  "NOTIFY_GUARD_RESULT" => "${{ needs.notify-guard.result }}",
  "PUBLISH_GATE_RESULT" => "${{ needs.publish-gate.result }}",
  "IMAGE_READY_RESULT" => "${{ needs.image-ready.result }}",
  "RELEASE_BINARIES_RESULT" => "${{ needs.release-binaries.result }}",
  "ROLLOUT_RESULT" => "${{ needs.rollout-verify.result }}",
  "FINALIZE_RESULT" => "${{ needs.finalize-release.result }}"
}.freeze
verify_guard = steps(jobs.fetch("verify-published")).find do |step|
  step["name"] == "Require every publication predecessor to succeed"
end
assert_policy(!verify_guard.nil? && verify_guard.fetch("env") == verify_predecessors &&
              verify_guard.fetch("run").include?('test "$result" = success'),
              "public verification must turn every failed or skipped predecessor into a workflow failure")

AGGREGATE_JOBS.each do |name|
  condition = jobs.fetch(name).fetch("if")
  assert_policy(condition.include?("always()") &&
                condition.include?("needs.publish-gate.outputs.recovery_mode"),
                "#{name} must explicitly select one successful publication path")
end

recovery_reachable_jobs = %w[
  recover-image image-ready binary-attestation-ready release-binaries
  rollout-verify finalize-release verify-published
]
recovery_reachable_jobs.each do |name|
  permissions = jobs.fetch(name).fetch("permissions", {})
  assert_policy(!permissions.key?("id-token") && !permissions.key?("attestations") &&
                !flattened_step_text(jobs.fetch(name)).include?("actions/attest@"),
                "#{name} must not receive OIDC or attestation mutation capability during recovery")
end

assert_policy((%w[publish-image recover-image] - jobs.fetch("image-ready").fetch("needs")).empty?,
              "image selector must require both mutually exclusive image paths")
assert_policy(jobs.fetch("attest-binaries").fetch("needs").include?("image-ready"),
              "normal binary attestation must follow immutable image selection")
assert_policy((%w[image-ready attest-binaries] -
               jobs.fetch("binary-attestation-ready").fetch("needs")).empty?,
              "binary attestation selector must require image selection and the tag-only attestation job")
assert_policy((%w[image-ready binary-attestation-ready] -
               jobs.fetch("release-binaries").fetch("needs")).empty?,
              "binary staging must follow immutable image and attestation selection")

%w[attest-binaries release-binaries].each do |name|
  binary_scan_steps = steps(jobs.fetch(name)).select do |step|
    step.fetch("uses", "").start_with?("aquasecurity/trivy-action@") &&
      step.fetch("with", {}).fetch("scan-ref", "") == "notify/dist"
  end
  assert_policy(binary_scan_steps.length == 2 &&
                binary_scan_steps.all? { |step| step.fetch("with").fetch("scan-type") == "rootfs" },
                "#{name} vulnerability and SBOM scans must inspect compiled Go binaries as root filesystems")
end

assert_policy((%w[release-binaries image-ready] - jobs.fetch("rollout-verify").fetch("needs")).empty?,
              "published rollout must follow complete draft staging and immutable image selection")
rollout_text = flattened_step_text(jobs.fetch("rollout-verify"))
assert_policy(rollout_text.include?("gh api graphql") &&
              rollout_text.include?("databaseId") &&
              rollout_text.include?('releases/${release_id}') &&
              rollout_text.include?('releases/assets/${asset_id}') &&
              rollout_text.include?('Accept: application/octet-stream') &&
              rollout_text.include?('test "$matches" = 1') &&
              rollout_text.include?('test "$(jq -r .draft <<<"$release_json")" = true') &&
              rollout_text.include?('^sha256:[0-9a-f]{64}$') &&
              rollout_text.include?("binary_sbom") &&
              rollout_text.include?("image_digests_sha256") &&
              !rollout_text.include?('gh release download "$RELEASE_TAG"'),
              "published rollout must resolve draft assets by validated release and asset IDs")
rollout_mutations = [
  "gh release ", "mutation(", "mutation ",
  "--method POST", "--method PATCH", "--method PUT", "--method DELETE",
  "-X POST", "-X PATCH", "-X PUT", "-X DELETE"
]
assert_policy(rollout_mutations.none? { |fragment| rollout_text.include?(fragment) },
              "published rollout may use draft visibility but must remain release-read-only")
assert_policy((%w[rollout-verify image-ready] - jobs.fetch("finalize-release").fetch("needs")).empty?,
              "release finalization must follow the published rollback proof")
assert_policy((%w[finalize-release image-ready] - jobs.fetch("verify-published").fetch("needs")).empty?,
              "read-only public verification must follow finalization")

gate_text = flattened_step_text(jobs.fetch("publish-gate"))
assert_policy(gate_text.include?("git merge-base --is-ancestor") &&
              gate_text.include?("refs/remotes/origin/main") &&
              gate_text.include?("refs/tags/${release_tag}^{commit}") &&
              gate_text.include?(".commit.verification.verified") &&
              gate_text.include?("$RELEASE_SPEC"),
              "publish gate must bind manifest, tag, verified SHA, and origin/main")
assert_policy(jobs.fetch("publish-gate").fetch("outputs").fetch("release_is_public") ==
                "${{ steps.validate.outputs.release_is_public }}" &&
              jobs.fetch("publish-gate").fetch("outputs").fetch("recovery_mode") ==
                "${{ steps.validate.outputs.recovery_mode }}" &&
              gate_text.include?("gh api graphql") &&
              gate_text.include?(".data.repository.release") &&
              gate_text.include?('case $release_type in') &&
              gate_text.include?('release_is_public=true') &&
              gate_text.include?('release_is_public=$release_is_public') &&
              gate_text.include?('[ "$recovery_mode" = true ] && [ "$release_type" = null ]') &&
              gate_text.include?("Draft visibility deferred to the exact recovery staging gate.") &&
              gate_text.include?('actions/runs/${RECOVERY_RUN_ID}') &&
              gate_text.include?('Create or verify the exact draft release and assets') &&
              gate_text.include?('git checkout --detach "$resolved"') &&
              gate_text.include?('recovery_mode=$recovery_mode'),
              "publish gate must fail closed and export the finalized public-release state")

assert_policy(spec.fetch("format_version") == 1, "release manifest format changed")
assert_policy(spec.fetch("version") == "2.0.0", "release version must remain 2.0.0")
assert_policy(spec.fetch("tag") == "notify-v2.0.0", "release tag must remain notify-v2.0.0")
assert_policy(spec.fetch("image") == "ghcr.io/psimaker/vaultsync-notify", "image repository changed")
assert_policy(spec.fetch("version_image") == "ghcr.io/psimaker/vaultsync-notify:2.0.0",
              "version image changed")
assert_policy(spec.fetch("binaries") == EXPECTED_BINARIES, "expected binary set or order changed")
assert_policy(spec.fetch("release_assets").sort == EXPECTED_ASSETS.sort, "release asset set changed")
rollback = spec.fetch("rollback")
assert_policy(rollback.fetch("tag") == "notify-v1.8.0", "rollback tag changed")
assert_policy(rollback.fetch("commit") == "e4f9e3088d7b7bc47943ff59db73de369c16c543",
              "rollback commit changed")
assert_policy(rollback.fetch("image") ==
              "ghcr.io/psimaker/vaultsync-notify@sha256:6e2b333dd16633d93c5104a72a2b133f4e0d95166757ee081930626e08858154",
              "rollback image digest changed")

dockerfile = File.read(DOCKERFILE_PATH)
assert_policy(dockerfile.include?("FROM scratch"), "release runtime must remain scratch-based")
assert_policy(!dockerfile.match?(/^RUN apk\b/), "release runtime must not consult apk repositories")
assert_policy(dockerfile.include?("USER 65534:65534"), "release runtime non-root identity changed")
assert_policy(dockerfile.include?("/etc/ssl/certs/ca-certificates.crt"), "pinned-builder CA copy is missing")

install_text = File.read(INSTALL_PATH)
compose_text = File.read(COMPOSE_PATH)
assert_policy(install_text.include?("ghcr.io/psimaker/vaultsync-notify:2.0.0"),
              "installer default is not the reviewed version tag")
assert_policy(compose_text.include?("ghcr.io/psimaker/vaultsync-notify:2.0.0"),
              "Compose default is not the reviewed version tag")
assert_policy(!install_text.include?("ghcr.io/psimaker/vaultsync-notify:latest") &&
              !compose_text.include?("ghcr.io/psimaker/vaultsync-notify:latest"),
              "an install path still follows the helper latest tag")
assert_policy(install_text.include?("runtime_image=$new_image_id"),
              "Docker installer must run the pulled immutable content ID")
assert_policy(!install_text.include?("continuing with the LOCAL image"),
              "Docker installer still permits a stale fallback")
assert_policy(install_text.include?("Could not fetch SHA256SUMS") &&
              install_text.include?("sha256sum or shasum is required") &&
              install_text.include?("matches != 1") &&
              install_text.include?("non-canonical checksum"),
              "binary installer no longer fails closed on checksum prerequisites")

security_text = flattened_step_text(security.fetch("jobs").fetch("image-scan"))
assert_policy(security_text.include?("IMAGE-DIGESTS"), "scheduled scan must resolve the release digest")
assert_policy(security_text.include?("vaultsync-notify@${{ steps.release-image.outputs.digest }}"),
              "scheduled scan must use the exact digest")
assert_policy(!security_text.include?("vaultsync-notify:latest"),
              "scheduled scan still follows latest")
assert_policy(security_text.include?("mapfile -t digests") &&
              security_text.include?('${#digests[@]}') &&
              security_text.include?("must contain exactly one index_digest") &&
              security_text.include?("^sha256:[0-9a-f]{64}$") &&
              security_text.include?("printf 'digest=%s\\n'"),
              "scheduled scan must export exactly one canonical image index digest")

ci_notify_steps = steps(ci.fetch("jobs").fetch("notify-tests"))
assert_policy(ci_notify_steps.any? { |step| step["run"] == "ruby .github/scripts/notify-publish-safety.rb" },
              "merge-blocking Notify Tests must execute the publish-safety policy")

owner = "repository-owner"
base = {
  event: "workflow_dispatch",
  ref_type: "tag",
  ref_name: spec.fetch("tag"),
  release_tag: spec.fetch("tag"),
  confirmation: "PUBLISH_NOTIFY_RELEASE",
  recovery_run_id: "",
  actor: owner,
  triggering_actor: owner,
  owner: owner
}
negative_cases = [
  base.merge(event: "push", ref_type: "branch", ref_name: "main"),
  base.merge(event: "pull_request", ref_type: "branch", ref_name: "pull/1/merge"),
  base.merge(ref_type: "branch", ref_name: "main"),
  base.merge(recovery_run_id: "29324314809"),
  base.merge(ref_type: "branch", ref_name: "main", recovery_run_id: "0"),
  base.merge(ref_type: "branch", ref_name: "main", recovery_run_id: "failed-run"),
  base.merge(release_tag: "notify-v2.0.1"),
  base.merge(confirmation: "publish"),
  base.merge(actor: "maintainer"),
  base.merge(triggering_actor: "maintainer"),
  base.merge(ref_name: "notify-v2.0", release_tag: "notify-v2.0")
]
negative_cases.each_with_index do |input, index|
  assert_policy(!publication_allowed?(**input), "negative gate case #{index + 1} unexpectedly publishes")
end
assert_policy(publication_allowed?(**base), "exact owner/tag/confirmation gate must remain reachable")
recovery_base = base.merge(ref_type: "branch", ref_name: "main", recovery_run_id: "29324314809")
assert_policy(publication_allowed?(**recovery_base), "exact owner/main/failed-run recovery gate must remain reachable")
assert_policy(resume_action(nil, "sha256:a", mutable: true) == :upload,
              "missing draft assets must be uploadable")
PUBLICATION_WRITE_KINDS.each do |kind|
  assert_policy(resume_action(nil, "sha256:a", mutable: false) == :abort,
                "missing public #{kind} must abort without mutation")
end
assert_policy(resume_action("sha256:a", "sha256:a", mutable: false) == :reuse,
              "identical public assets must be reusable read-only")
assert_policy(resume_action("sha256:b", "sha256:a", mutable: true) == :abort,
              "different assets must abort even while draft")

puts "notify publish safety policy: ok (#{negative_cases.length} negative gates, exact manifest, immutable resume model)"
