/*
  This script only shows that a form is working. Every form also works without
  it. A button with data-busy shows that text from the moment it is pressed,
  and every other button of its group stops working, so a second press does
  nothing. The screen reader hears the same text.

  The browser gives no sign when the user stops a request, so the buttons come
  back after Esc, after releaseAfterMs, or when the back button shows the page
  again. This is safe: the server carries out an action only once, and refuses
  a second answer.

  It never reads or sends anything by itself: the browser posts the form.
*/
"use strict";

(() => {
  const slowAfterMs = 10000;
  const releaseAfterMs = 30000;
  const slowText = "This is taking longer than usual. Keep this page open.";
  const releaseText = "No answer yet. Reload this page to see the result.";

  // The form that is working: its button, its text, the locked buttons and the
  // timers. It is null while no form is working.
  let working = null;

  // The screen reader hears the text. The button already shows it on the page.
  function announce(text) {
    const live = document.querySelector("[data-live]");
    if (live) {
      live.textContent = text;
    }
  }

  // The note says what happens before the answer, so it goes when the button
  // is pressed. A slow request fills it again.
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

  // release gives the buttons back. It does nothing while no form is working.
  function release() {
    if (!working) {
      return;
    }
    const { button, label, locked, timers } = working;
    working = null;
    timers.forEach(clearTimeout);
    for (const other of locked) {
      other.removeAttribute("aria-disabled");
      other.classList.remove("is-locked");
    }
    button.classList.remove("is-busy");
    button.style.minWidth = "";
    button.textContent = label;
    setNote(releaseText);
    announce(releaseText);
  }

  document.addEventListener("submit", (event) => {
    if (working) {
      event.preventDefault();
      return;
    }
    const form = event.target;
    const button = event.submitter ?? form.querySelector("button[type=submit]");
    if (!button || !button.dataset.busy) {
      return;
    }
    const group = form.closest("[data-busy-group]") ?? form;
    const locked = [...group.querySelectorAll("button")];
    for (const other of locked) {
      other.setAttribute("aria-disabled", "true");
      other.classList.add("is-locked");
    }
    working = {
      button,
      label: button.textContent,
      locked,
      timers: [setTimeout(warnSlow, slowAfterMs), setTimeout(release, releaseAfterMs)],
    };
    // The button keeps its width, so the bar does not jump.
    button.style.minWidth = `${button.offsetWidth}px`;
    button.classList.add("is-busy");
    button.textContent = button.dataset.busy;
    setNote("");
    announce(button.dataset.busy);
  });

  // Esc stops a request in a browser.
  document.addEventListener("keydown", (event) => {
    if (event.key === "Escape") {
      release();
    }
  });

  // The back button can show a page from the memory of the browser, as it was
  // when it was left. A reload could post a form again, so the page only gives
  // its buttons back. The server refuses an answer that is too late.
  window.addEventListener("pageshow", (event) => {
    if (event.persisted) {
      release();
    }
  });

  // The result of an action is new on the page, so it gets the keyboard focus,
  // and the screen reader reads it.
  document.querySelector("[data-focus]")?.focus();
})();
