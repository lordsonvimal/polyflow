# Negative fixture: these must NOT match any nav_link_rails_* pattern.
# Bare method calls that aren't nav helpers:
respond_to :html
render :show
# Variable assignment that happens to hold a path:
path = reports_path
url = "/reports"
# link_to with only one argument (no target):
link_to "Home"
# Unrelated method with "to" in name:
belongs_to :user
# redirect_to targets the query itself rejects: a receiver call is not a route
# helper, and a symbol is not a destination this graph can name. Targets that
# are shaped like a bare call but name a local (`redirect_to trash_folder`) do
# match here and are dropped a layer later, by the `_path`/`_url` suffix gate in
# ruby_nav_helper_gate.go — see its test for those.
redirect_to request.referer
redirect_to @folder.uri
redirect_to :back

# RT.2: a single-argument `#` names no route and no request. The two-argument
# form (`link_to text, "#"`) still matches — see the pattern comment.
link_to("#", class: "btn")
link_to "#" do
end
