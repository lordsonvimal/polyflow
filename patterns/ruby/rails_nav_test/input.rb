# Positive fixture: Rails navigation helpers and literal paths
link_to "Reports", reports_path
link_to "Report", report_path(@report)
button_to "Delete", report_path(@report)
link_to "New Report", new_report_path
link_to "Archive", archive_report_path
link_to "Home", "/home"
button_to "Submit", "/submit"
form_with url: "/users", method: :post do
end
form_with url: reports_path do
end
form_for @user, url: users_path do
end

# C.3: redirect_to is a nav producer — a 302 is a page-to-page transition, and
# the browser follows it with GET against the same origin.
redirect_to dashboard_path
redirect_to folder_path(@folder), notice: t("folders.created")
redirect_to "/saml/login"
