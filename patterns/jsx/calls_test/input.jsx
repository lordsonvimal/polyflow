function UserList({ onSelect }) {
  fetchUsers();
  window.maple.queueToast("done");
  return (
    <div>
      <button onClick={onSelect}>Click</button>
      <button on:click={onSelect}>Delegated</button>
    </div>
  );
}
