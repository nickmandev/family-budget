#!/usr/bin/env bash
# Derive the next release version from Conventional Commits since the newest
# stable tag reachable from HEAD. Emits should_release, version and tag to
# GITHUB_OUTPUT when set, and always prints the same three lines to stdout.

set -euo pipefail

emit() {
	printf '%s\n' "$1"
	if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
		printf '%s\n' "$1" >> "$GITHUB_OUTPUT"
	fi
}

bump=0

# Conventional Commit subject patterns. Quoted variables keep bash's [[ =~ ]]
# parser away from the regex parentheses.
breaking_re='^[a-z]+(\([^)]*\))?!:'
feat_re='^feat(\([^)]*\))?:'
patch_re='^(fix|perf)(\([^)]*\))?:'

# classify maps one commit (subject + body) to a bump level:
# 3 major, 2 minor, 1 patch, 0 ignore. The highest level wins.
classify() {
	local subject="$1" body="$2" level=0
	if [[ "$subject" =~ $breaking_re ]] ||
		[[ "$body" == *"BREAKING CHANGE:"* || "$body" == *"BREAKING-CHANGE:"* ]]; then
		level=3
	elif [[ "$subject" =~ $feat_re ]]; then
		level=2
	elif [[ "$subject" =~ $patch_re ]]; then
		level=1
	fi
	if (( level > bump )); then
		bump=$level
	fi
}

if ! git rev-parse --verify --quiet HEAD >/dev/null; then
	emit 'should_release=false'
	emit 'version='
	emit 'tag='
	exit 0
fi

tags="$(git tag --merged HEAD --list 'v*' --sort=-v:refname |
	grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' || true)"
last_tag="$(printf '%s\n' "$tags" | head -n 1)"
if [[ -n "$last_tag" ]]; then
	base="${last_tag#v}"
	range="$last_tag..HEAD"
else
	base="0.0.0"
	range="HEAD"
fi

subject=""
body=""
while IFS= read -r line; do
	if [[ "$line" == '--COMMIT--' ]]; then
		classify "$subject" "$body"
		subject=""
		body=""
	elif [[ -z "$subject" ]]; then
		subject="$line"
	else
		body+="$line"$'\n'
	fi
done < <(git log --format='%s%n%b%n--COMMIT--' "$range")

should_release=false
version=""
tag=""
if (( bump > 0 )); then
	IFS='.' read -r major minor patch <<< "$base"
	case $bump in
		3) version="$((major + 1)).0.0" ;;
		2) version="$major.$((minor + 1)).0" ;;
		1) version="$major.$minor.$((patch + 1))" ;;
	esac
	tag="v$version"
	if [[ -n "$(git tag --list "$tag")" ]]; then
		printf 'Refusing to release: the computed tag %s already exists. Remove it deliberately or add more commits.\n' "$tag" >&2
		exit 1
	fi
	should_release=true
fi

emit "should_release=$should_release"
emit "version=$version"
emit "tag=$tag"
