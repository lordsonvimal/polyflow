<template>
  <div>
    <button @click="increment">+</button>
  </div>
</template>

<script lang="ts">
import { ref } from 'vue'

export default {
  setup() {
    const count = ref(0)
    function increment(): void { count.value++ }
    return { count, increment }
  }
}
</script>
