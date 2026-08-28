# Negative fixture: ordinary commands that are neither a recognized
# invocation verb (bash/sh/source/.) nor a bare "./"/"../"-prefixed target.
echo hello
git status
curl https://example.com
cd /tmp
bin/setup
