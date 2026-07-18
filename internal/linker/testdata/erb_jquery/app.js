$("#save-btn").on("click", function () {
  $.ajax({ url: "/save", method: "POST" });
});
