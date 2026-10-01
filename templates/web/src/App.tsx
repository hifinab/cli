import { useState } from "react";

export function App() {
  const [count, setCount] = useState(0);

  return (
    <main>
      <h1>Hifin Template Name</h1>
      <button type="button" onClick={() => setCount(count + 1)}>
        Clicked {count} times
      </button>
    </main>
  );
}
