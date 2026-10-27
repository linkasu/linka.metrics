"""Skip automatic publication only for the reviewed licensing snapshot.

The exemption matches only this snapshot directly above BASE; normal runtime
changes and explicit workflow_dispatch retain their publication behavior.
shortcut: this exemption is snapshot-bound; refresh BASE/hashes if rebased.
"""
import hashlib
import os
from pathlib import Path
import subprocess
import sys

BASE = 'e32585d667463065e862ba2066a06eb107be3c40'
EXPECTED = {
    ".github/workflows/deploy-vps.yml": "0a818a1a6df39b6ada2635900a9d4839baeaf348dcf725bae8b2e3ed44bd4f12",
    ".github/workflows/deploy-yc.yml": "9c9597a6d966f78251fc70e8fda02a6a3ce0cdce94bb502cfaf0ffce72391fb7",
    ".github/workflows/publish.yml": "ade5a542f74b1a76a38f3799e871775f575d7a205b03ddd324aa69c9c446ab2c",
    "LICENSE": "3972dc9744f6499f0f9b2dbf76696f2ae7ad8af9b23dde66d6af86c9dfb36986",
    "README.md": "855ac349c5a92a709827bfd76c05d195afdc701a2a74e08fe892f135d0eaeefe",
    "THIRD_PARTY_NOTICES.md": "024dedc9d1783b5a80db66ac17012fa89b820f37217840f8b459f117a448ab1b"
}
SCRIPT = '.github/scripts/licensing-deployment-guard.py'


def licensing_only(parent, files):
    return parent == BASE and files == EXPECTED


def self_test():
    assert licensing_only(BASE, dict(EXPECTED))
    assert not licensing_only('another-parent', dict(EXPECTED))
    assert not licensing_only(BASE, {**EXPECTED, 'runtime.py': 'changed'})
    assert not licensing_only(BASE, {**EXPECTED, 'package.json': 'changed'})
    assert not licensing_only(BASE, {})
    print('licensing deployment guard: 5 checks passed')


def should_deploy():
    event = os.environ.get('GITHUB_EVENT_NAME')
    if event not in ('push', 'workflow_run', 'pull_request') or os.environ.get('GITHUB_REF_TYPE') == 'tag':
        return True
    parent = subprocess.check_output(['git', 'rev-parse', 'HEAD^'], text=True).strip()
    if parent != BASE:
        return True
    paths = subprocess.check_output(
        ['git', 'diff', '--name-only', '-z', BASE, 'HEAD']
    ).decode().split('\0')[:-1]
    if SCRIPT not in paths:
        return True
    files = {
        path: hashlib.sha256(Path(path).read_bytes()).hexdigest()
        for path in paths if path != SCRIPT
    }
    return not licensing_only(parent, files)


if __name__ == '__main__':
    if '--self-test' in sys.argv:
        self_test()
    else:
        deploy = should_deploy()
        print('deploy=' + str(deploy).lower())
        with open(os.environ['GITHUB_OUTPUT'], 'a') as output:
            output.write('deploy=' + str(deploy).lower() + '\n')
