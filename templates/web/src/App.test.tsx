import { fireEvent, render, screen } from "@testing-library/react";
import { expect, test } from "vitest";
import { App } from "./App";

test("counts clicks", () => {
  render(<App />);
  fireEvent.click(screen.getByRole("button"));
  expect(screen.getByRole("button").textContent).toBe("Clicked 1 times");
});
