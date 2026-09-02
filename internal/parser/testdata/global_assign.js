async function _fetchAppConfig(configId) {
  const resp = await fetch("/api/v1/app-configs/" + configId);
  return resp.json();
}

window.maple = window.maple || {};

window.maple.openAppConfigForEdit = async function (configId) {
  var config = await _fetchAppConfig(configId);
  if (!config) return;
  _populateAppConfigEditForm(config, "edit");
};

function _populateAppConfigEditForm(config, mode) {}
