# Negative fixture: these must NOT match any nav_link_rails_* pattern.
# Bare method calls that aren't nav helpers:
redirect_to root_path
respond_to :html
render :show
# Variable assignment that happens to hold a path:
path = reports_path
url = "/reports"
# link_to with only one argument (no target):
link_to "Home"
# Unrelated method with "to" in name:
belongs_to :user
