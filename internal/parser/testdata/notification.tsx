import { Component, onMount } from "solid-js";

async function fetchWarnings() {
  const res = await fetch("/api/stats");
  return res.json();
}

const Notification: Component = () => {
  onMount(() => {
    fetchWarnings();
  });

  return <div>notification</div>;
};

export default Notification;
