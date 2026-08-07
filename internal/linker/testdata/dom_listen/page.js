// Delegated: the element listened to is the SECOND argument, not the receiver.
$(document).on("click", ".js-remove-track", function (e) {
  e.preventDefault();
  $.ajax({ url: "/tracks/remove", method: "DELETE" });
});

// Non-delegated: the element is the receiver's own selector.
$("#approve-btn").on("click", function () {
  $.ajax({ url: "/approve", method: "POST" });
});

// Shorthand: the event name sits in the method position.
$(".js-approve").change(function () {
  reportChange();
});

// A named handler resolves to the function it names.
$("#approve-btn").on("mouseenter", showHint);

function showHint() {}
function reportChange() {}

// No element to look for: a document-scoped listener with no delegated
// selector names nothing, so it is not a ledger entry either.
$(document).on("keydown", function () {});

// A bare tag selector names an element type, which the id/class index cannot
// hold — nothing to resolve, so nothing to ledger either.
$("body").on("mouseup", function () {});

// A selector held in a variable is a clue this pass failed to resolve.
$(rowSelector).on("click", function () {});
