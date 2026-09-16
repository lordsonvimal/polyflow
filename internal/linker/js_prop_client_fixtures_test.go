package linker

// ajaxStatusHOC is the minimal shape of a transport-forwarding HOC: a
// `Name(Wrapped)` function that renders `<Wrapped ajaxStatus={this} />` and
// declares a class whose `get(msg, url)` reaches `window.$.ajax` via `ajax`.
//
// Kept here as pure fixture text after internal/linker/js_prop_client.go's
// own pass (LinkJSPropClients) migrated to Tier FX
// (internal/factpipe/hub_schema_url_link.go) — vg_crossing_test.go includes
// this file in its fixtures for realism without ever calling
// LinkJSPropClients itself (it drives LinkJSPropURLs/LinkJSPropTransport
// directly), so it doesn't need porting, just this constant to still exist.
const ajaxStatusHOC = `import React from "react";
const ComponentWithAjaxStatus = function (WrappedComponent) {
  class component extends React.Component {
    get = (msg, url) => {
      const request = { method: "GET", url };
      return this.ajax(msg, request);
    };
    ajax = (msg, request) => {
      return window.$.ajax(request);
    };
    render() {
      return (
        <div>
          <WrappedComponent ajaxStatus={this} {...this.props} />
        </div>
      );
    }
  }
  return component;
};
export default ComponentWithAjaxStatus;
`
