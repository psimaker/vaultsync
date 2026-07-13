#!/usr/bin/env ruby
# frozen_string_literal: true

require "yaml"

ROOT = File.expand_path("../..", __dir__)
WORKFLOW_PATH = File.join(ROOT, ".github/workflows/docker.yml")
CI_PATH = File.join(ROOT, ".github/workflows/ci.yml")
PINNED_ACTION = /\A[^@]+@[0-9a-f]{40}\z/
PUBLISH_JOBS = %w[publish-image release-binaries].freeze
GATE_FRAGMENTS = [
  "github.event_name == 'workflow_dispatch'",
  "github.ref_type == 'tag'",
  "inputs.release_tag == github.ref_name",
  "inputs.confirmation == 'PUBLISH_NOTIFY_RELEASE'",
  "github.actor == github.repository_owner",
  "github.triggering_actor == github.repository_owner"
].freeze

def fail_policy(message)
  warn "notify publish safety policy: #{message}"
  exit 1
end

def assert_policy(condition, message)
  fail_policy(message) unless condition
end

def load_workflow(path)
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

workflow = load_workflow(WORKFLOW_PATH)
ci = load_workflow(CI_PATH)
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
guarded_paths = %w[notify/** .github/workflows/docker.yml .github/workflows/ci.yml .github/scripts/notify-publish-safety.rb]
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

required_jobs = %w[publish-safety-policy notify-guard build-without-push publish-gate publish-image release-binaries]
assert_policy((required_jobs - jobs.keys).empty?, "required publication-safety jobs are missing")
assert_policy(jobs.fetch("build-without-push").fetch("if") == "github.event_name != 'workflow_dispatch'",
              "the no-push build must be the only image build on normal events")
assert_policy(jobs.fetch("publish-gate").fetch("if") == "github.event_name == 'workflow_dispatch'",
              "the read-only publish gate must run only for manual dispatch")

write_jobs = jobs.select do |_name, job|
  job.fetch("permissions", {}).value?("write")
end.keys.sort
assert_policy(write_jobs == PUBLISH_JOBS.sort,
              "only publish-image and release-binaries may receive write permissions")
assert_policy(jobs.fetch("publish-image").fetch("permissions") == {
                "contents" => "read",
                "packages" => "write",
                "id-token" => "write",
                "attestations" => "write"
              }, "publish-image permissions changed")
assert_policy(jobs.fetch("release-binaries").fetch("permissions") == { "contents" => "write" },
              "release-binaries permissions changed")

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
assert_policy(login_jobs == ["publish-image"], "registry login must exist only in publish-image")
release_jobs = jobs.select { |_name, job| flattened_step_text(job).include?("gh release ") }.keys
assert_policy(release_jobs == ["release-binaries"], "GitHub release mutation must exist only in release-binaries")

normal_text = jobs.reject { |name, _job| PUBLISH_JOBS.include?(name) }
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

PUBLISH_JOBS.each do |name|
  job = jobs.fetch(name)
  condition = job.fetch("if")
  GATE_FRAGMENTS.each do |fragment|
    assert_policy(condition.include?(fragment), "#{name} is missing gate condition #{fragment}")
  end
  assert_policy((%w[publish-safety-policy notify-guard publish-gate] - job.fetch("needs")).empty?,
                "#{name} must depend on policy, tests, and the owner gate")
end

gate_text = flattened_step_text(jobs.fetch("publish-gate"))
assert_policy(gate_text.include?("git merge-base --is-ancestor") &&
              gate_text.include?("refs/remotes/origin/main") &&
              gate_text.include?("refs/tags/${RELEASE_TAG}^{commit}"),
              "publish gate must bind the selected tag SHA to origin/main")

ci_notify_steps = steps(ci.fetch("jobs").fetch("notify-tests"))
assert_policy(ci_notify_steps.any? { |step| step["run"] == "ruby .github/scripts/notify-publish-safety.rb" },
              "merge-blocking Notify Tests must execute the publish-safety policy")

owner = "repository-owner"
base = {
  event: "workflow_dispatch",
  ref_type: "tag",
  ref_name: "notify-v2.0.0",
  release_tag: "notify-v2.0.0",
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

puts "notify publish safety policy: ok (#{negative_cases.length} negative cases, 1 positive model case)"
