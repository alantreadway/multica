#!/usr/bin/env bash
# deploy-selfhost.sh: retired 2026-11. It deployed to the Colima self-host on the
# work Mac, which moved to Bastion. See README.md, "Deploy to the self-host".
echo "deploy-selfhost.sh is retired: the self-host runs on Bastion now." >&2
echo "Use ~/git/home-sysadmin/scripts/multica-ag/deploy.sh <version> <rev>, /bastion-push, then deploy.sh --apply <version>." >&2
exit 2
