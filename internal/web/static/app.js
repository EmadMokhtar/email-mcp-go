/*
  This script only shows that a form is working. Every form also works without
  it. A button with data-busy shows that text from the moment it is pressed,
  and every other button of its group stops working, so nothing is sent twice.
  The screen reader hears the same text.

  It never reads or sends anything by itself: the browser posts the form.
*/
"use strict";

(() => {
  const slowAfterMs = 10000;
  const slowText = "This is taking longer than usual. Keep this page open.";
  let busy = false;

  // The screen reader hears the text. The button already shows it on the page.
  function announce(text) {
    const live = document.querySelector("[data-live]");
    if (live) {
      live.textContent = text;
    }
  }

  // The note beside the buttons says what happens before the answer, so it goes
  // when the button is pressed. A slow request fills it again.
  function setNote(text) {
    const note = document.querySelector("[data-note]");
    if (note) {
      note.textContent = text;
    }
  }

  function warnSlow() {
    setNote(slowText);
    announce(slowText);
  }

  document.addEventListener("submit", (event) => {
    if (busy) {
      event.preventDefault();
      return;
    }
    const form = event.target;
    const button = event.submitter ?? form.querySelector("button[type=submit]");
    if (!button || !button.dataset.busy) {
      return;
    }
    busy = true;
    const group = form.closest("[data-busy-group]") ?? form;
    for (const other of group.querySelectorAll("button")) {
      other.setAttribute("aria-disabled", "true");
      other.classList.add("is-locked");
    }
    // The button keeps its width, so the bar does not jump.
    button.style.minWidth = `${button.offsetWidth}px`;
    button.classList.add("is-busy");
    button.textContent = button.dataset.busy;
    setNote("");
    announce(button.dataset.busy);
    setTimeout(warnSlow, slowAfterMs);
  });

  // The back button can show a page from the memory of the browser, as it was
  // when it was left. Load it again, so it does not show a form that is working.
  window.addEventListener("pageshow", (event) => {
    if (event.persisted) {
      location.reload();
    }
  });

  // The result of an action is new on the page, so it gets the keyboard focus,
  // and the screen reader reads it.
  document.querySelector("[data-focus]")?.focus();
})();
