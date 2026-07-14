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
WRITE_JOBS = %w[publish-image release-binaries finalize-release].freeze
DISPATCH_JOBS = %w[
  publish-image release-binaries rollout-verify finalize-release verify-published
].freeze
MANUAL_JOBS = (DISPATCH_JOBS + ["publish-gate"]).freeze
GATE_FRAGMENTS = [
  "github.event_name == 'workflow_dispatch'",
  "github.ref_type == 'tag'",
  "inputs.release_tag == github.ref_name",
  "inputs.confirmation == 'PUBLISH_NOTIFY_RELEASE'",
  "github.actor == github.repository_owner",
  "github.triggering_actor == github.repository_owner"
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

def publication_allowed?(event:, ref_type:, ref_name:, release_tag:, confirmation:, actor:, triggering_actor:, owner:)
  event == "workflow_dispatch" &&
    ref_type == "tag" &&
    release_tag == ref_name &&
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

required_jobs = %w[
  publish-safety-policy notify-guard build-without-push publish-gate publish-image
  release-binaries rollout-verify finalize-release verify-published
]
assert_policy((required_jobs - jobs.keys).empty?, "required publication-safety jobs are missing")
assert_policy(jobs.fetch("build-without-push").fetch("if") == "github.event_name != 'workflow_dispatch'",
              "the no-push build must be the only image build on normal events")
assert_policy(jobs.fetch("publish-gate").fetch("if") == "github.event_name == 'workflow_dispatch'",
              "the read-only publish gate must run only for manual dispatch")
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
              "only the image, binary staging, and finalization jobs may receive write permissions")
assert_policy(jobs.fetch("publish-image").fetch("permissions") == {
                "contents" => "read",
                "packages" => "write",
                "id-token" => "write",
                "attestations" => "write"
              }, "publish-image permissions changed")
assert_policy(jobs.fetch("release-binaries").fetch("permissions") == {
                "contents" => "write",
                "id-token" => "write",
                "attestations" => "write"
              }, "release-binaries permissions changed")
assert_policy(jobs.fetch("finalize-release").fetch("permissions") == { "contents" => "write" },
              "finalize-release permissions changed")

jobs.each do |name, job|
  steps(job).each do |step|
    action = step["uses"]
    next unless action

    assert_policy(action.match?(PINNED_ACTION), "#{name} uses a non-immutable action ref: #{action}")
    next unless action.start_with?("actions/checkout@")

    assert_policy(step.fetch("with", {})["persist-credentials"] == false,
                  "#{name} checkout must set persist-credentials: false")
  end
end

login_jobs = jobs.select { |_name, job| flattened_step_text(job).include?("docker/login-action@") }.keys
assert_policy(login_jobs == %w[publish-image verify-published],
              "registry login must exist only in image publication and read-only public verification")
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

DISPATCH_JOBS.each do |name|
  job = jobs.fetch(name)
  condition = job.fetch("if")
  GATE_FRAGMENTS.each do |fragment|
    assert_policy(condition.include?(fragment), "#{name} is missing gate condition #{fragment}")
  end
  assert_policy((%w[publish-safety-policy notify-guard publish-gate] - job.fetch("needs")).empty?,
                "#{name} must depend on policy, tests, and the owner gate")
end
assert_policy(jobs.fetch("release-binaries").fetch("needs").include?("publish-image"),
              "binary staging must follow image publication")
binary_scan_steps = steps(jobs.fetch("release-binaries")).select do |step|
  step.fetch("uses", "").start_with?("aquasecurity/trivy-action@") &&
    step.fetch("with", {}).fetch("scan-ref", "") == "notify/dist"
end
assert_policy(binary_scan_steps.length == 2 &&
              binary_scan_steps.all? { |step| step.fetch("with").fetch("scan-type") == "rootfs" },
              "binary vulnerability and SBOM scans must inspect compiled Go binaries as root filesystems")
assert_policy(jobs.fetch("rollout-verify").fetch("needs").include?("release-binaries"),
              "published rollout must follow complete draft staging")
assert_policy(jobs.fetch("finalize-release").fetch("needs").include?("rollout-verify"),
              "release finalization must follow the published rollback proof")
assert_policy(jobs.fetch("verify-published").fetch("needs").include?("finalize-release"),
              "read-only public verification must follow finalization")

gate_text = flattened_step_text(jobs.fetch("publish-gate"))
assert_policy(gate_text.include?("git merge-base --is-ancestor") &&
              gate_text.include?("refs/remotes/origin/main") &&
              gate_text.include?("refs/tags/${release_tag}^{commit}") &&
              gate_text.include?(".commit.verification.verified") &&
              gate_text.include?("$RELEASE_SPEC"),
              "publish gate must bind manifest, tag, verified SHA, and origin/main")

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
  actor: owner,
  triggering_actor: owner,
  owner: owner
}
negative_cases = [
  base.merge(event: "push", ref_type: "branch", ref_name: "main"),
  base.merge(event: "pull_request", ref_type: "branch", ref_name: "pull/1/merge"),
  base.merge(ref_type: "branch", ref_name: "main", release_tag: "main"),
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
assert_policy(resume_action(nil, "sha256:a", mutable: true) == :upload,
              "missing draft assets must be uploadable")
assert_policy(resume_action(nil, "sha256:a", mutable: false) == :abort,
              "missing public assets must abort without mutation")
assert_policy(resume_action("sha256:a", "sha256:a", mutable: false) == :reuse,
              "identical public assets must be reusable read-only")
assert_policy(resume_action("sha256:b", "sha256:a", mutable: true) == :abort,
              "different assets must abort even while draft")

puts "notify publish safety policy: ok (#{negative_cases.length} negative gates, exact manifest, immutable resume model)"
