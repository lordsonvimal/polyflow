// No id or className props — data-* attrs and htmlFor should not match.
function Form() {
  return (
    <div data-testid="form" aria-label="form">
      <label htmlFor="email">Email</label>
      <input type="email" name="email" placeholder="Enter email" />
    </div>
  );
}
