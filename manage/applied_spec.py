import logging
import re
from dataclasses import dataclass
from datetime import timedelta
from typing import ClassVar, Sequence

from applied_manage.arch import MULTIARCH, Arch
from applied_manage.base_service import (
    NO_DEPLOYMENT_SERVICE,
    BaseApiService,
    BaseContainer,
    BuildContext,
    DockerContainer,
    PeriodicDeploy,
)
from applied_manage.base_spec import BaseTestSpec
from applied_manage.deploy.env import DeployEnv
from applied_manage.repo.utils import repo_git_sha
from applied_manage.service_testing import TestContext
from applied_manage.types import Owner, ServiceTier
from oai_clusters import ClusterType
from oai_docker_utils import docker_buildx_tag
from oai_owners.owners import API_ENTERPRISE


@dataclass
class TunnelClientTests(BaseTestSpec):
    name = "tunnel-client-tests"
    owner: ClassVar[Owner] = API_ENTERPRISE
    pyproject_relative_path = "api/tunnel-client/pyproject.toml"
    bazel_test_targets = ["//api/tunnel-client/..."]
    dockerfile_relative_path = "api/tunnel-client/Dockerfile"
    dockerfile_target = "unittest"
    dockerfile_variables = {
        "PROJECT_ROOT": "api/tunnel-client",
        "BASE_BUILDER_IMAGE": "golang:1.26.2-alpine",
        "BASE_IMAGE": "alpine:3.22",
    }

    def __post_init__(self) -> None:
        super().__post_init__()
        self.pre_build = self._pre_build

    def _pre_build(self) -> None:
        # Keep unit test images aligned with the tunnel-client binary metadata.
        self.dockerfile_variables["GIT_SHA"] = repo_git_sha()

    def _test(self, ctx: TestContext) -> None:
        ctx.run(cmd=["/bin/sh", "-c", "test -x /usr/bin/tunnel-client"], key="check-exec")
        ctx.run(cmd=["/bin/sh", "-c", "/usr/bin/tunnel-client --help"], key="check-binary-runs")


@dataclass
class TunnelClientImage(BaseApiService):
    name: ClassVar[str] = "tunnel-client"
    owner: ClassVar[Owner] = API_ENTERPRISE
    service_tier: ServiceTier = ServiceTier.TIER_3
    kube_yaml_relative_path: str = NO_DEPLOYMENT_SERVICE
    cluster_types = [ClusterType.Other]
    arch: Arch = MULTIARCH
    # Add a staging periodic deploy solely so Buildkite produces images after PRs merge.
    periodic_deploys: ClassVar[list[PeriodicDeploy]] = [
        PeriodicDeploy(envs={DeployEnv.STAGING}, frequency=timedelta(days=7), all_hours=True),
    ]

    buildkite_emoji: ClassVar[str] = ":docker:"
    build_hooks_can_run_in_ci: ClassVar[bool] = True

    containers: Sequence[BaseContainer] = (
        DockerContainer(
            name="tunnel-client",
            pyproject_relative_path="api/tunnel-client/pyproject.toml",
            dockerfile_relative_path="api/tunnel-client/Dockerfile",
            dockerfile_variables={
                "PROJECT_ROOT": "api/tunnel-client",
                "BASE_BUILDER_IMAGE": "golang:1.26.2-alpine",
                "BASE_IMAGE": "alpine:3.22",
            },
        ),
    )

    def __post_init__(self):
        super().__post_init__()
        self.pre_build = self._pre_build

        def _tag_and_push_latest(ctx):
            # Skip test images
            if ctx.tag.endswith(".tests"):
                return
            source_image = ctx.tag  # manifest tag for multi-arch image
            target_image = f"{source_image.rsplit(':', 1)[0]}:latest"
            logging.getLogger(__name__).info(
                "Retagging %s -> %s for tunnel-client", source_image, target_image
            )
            try:
                docker_buildx_tag(
                    source_tag=source_image,
                    target_tag=target_image,
                    retry_after_login=True,
                )
            except Exception as e:
                logging.getLogger(__name__).warning(
                    "Failed to tag latest for %s -> %s: %r (non-fatal)",
                    source_image,
                    target_image,
                    e,
                )

        self.post_manifest = _tag_and_push_latest

    def _pre_build(self, ctx: BuildContext) -> None:
        git_sha_match: re.Match[str] | None = re.search("[a-f0-9]{40}", ctx.tag)
        try:
            fallback_sha = ctx.tag.split(".")[1]
        except IndexError:
            fallback_sha = ctx.tag
        git_sha = git_sha_match.group(0) if git_sha_match else fallback_sha

        for container in self.containers:
            assert isinstance(container, DockerContainer)
            container.dockerfile_variables.update(
                {
                    "GIT_SHA": git_sha,
                },
            )
