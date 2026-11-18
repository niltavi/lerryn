// From the Niltavi UI templates (src/templates/saas/ForestScene.tsx, MIT); see design/niltavi-ui.
import {useId} from "react";

/**
 * Decorative brand panel: a misty stand of tall trees, two shafts of light, and a small niltava
 * resting on a branch inside the light. Flat geometric shapes only; the light breathes slowly.
 */
export function ForestScene() {
  const id = useId().replace(/:/g, "");
  const ref = (name: string) => `url(#${id}-${name})`;
  return <svg className="nt-forest" viewBox="0 0 400 640" preserveAspectRatio="xMidYMid slice" aria-hidden="true" focusable="false">
    <defs>
      <linearGradient id={`${id}-air`} x1="0" y1="0" x2="0" y2="1">
        <stop offset="0" className="nt-forest__air-top" />
        <stop offset=".74" className="nt-forest__air-low" />
      </linearGradient>
      <linearGradient id={`${id}-mist`} x1="0" y1="0" x2="0" y2="1">
        <stop offset="0" className="nt-forest__mist-stop" stopOpacity="0" />
        <stop offset=".55" className="nt-forest__mist-stop" stopOpacity=".9" />
        <stop offset="1" className="nt-forest__mist-stop" stopOpacity="0" />
      </linearGradient>
      <linearGradient id={`${id}-fade`} x1="0" y1="0" x2="0" y2="1">
        <stop offset="0" className="nt-forest__fade-stop" stopOpacity="0" />
        <stop offset="1" className="nt-forest__fade-stop" stopOpacity="1" />
      </linearGradient>
    </defs>
    <rect width="400" height="640" fill={ref("air")} />
    <g className="nt-forest__far">
        <path d="M20.7 -10H26.3L27.0 474H20.0Z" />
        <path d="M52.5 -10H56.5L57.0 474H52.0Z" />
        <path d="M95.8 -10H102.2L103.0 474H95.0Z" />
        <path d="M124.4 -10H127.6L128.0 474H124.0Z" />
        <path d="M178.9 -10H186.1L187.0 474H178.0Z" />
        <path d="M232.6 -10H237.4L238.0 474H232.0Z" />
        <path d="M262.8 -10H269.2L270.0 474H262.0Z" />
        <path d="M300.5 -10H304.5L305.0 474H300.0Z" />
        <path d="M338.7 -10H344.3L345.0 474H338.0Z" />
        <path d="M378.6 -10H383.4L384.0 474H378.0Z" />
    </g>
    <rect y="472" width="400" height="168" className="nt-forest__ground" />
    <rect y="372" width="400" height="128" fill={ref("mist")} />
    <g className="nt-forest__shade">
        <path d="M-4.0 500H14.0L-56.0 546H-74.0Z" />
        <path d="M70.0 494H86.0L16.0 540H0.0Z" />
        <path d="M150.0 486H163.0L93.0 532H80.0Z" />
        <path d="M254.0 488H271.0L201.0 534H184.0Z" />
        <path d="M296.0 492H310.0L240.0 538H226.0Z" />
        <path d="M356.0 504H376.0L306.0 550H286.0Z" />
        <path d="M108 610H142L52 670H18Z" />
    </g>
    <path d="M140 640C186 592 250 562 228 522C214 498 199 486 203 472H209C215 488 240 500 252 522C274 566 232 604 276 640Z" className="nt-forest__path" />
    <g className="nt-forest__beams">
      <path d="M314-10H360L146 472H88Z" />
      <path d="M372-10H392L214 472H188Z" />
    </g>
    <g className="nt-forest__mid">
        <path d="M-1.8 -10H11.8L14.0 500H-4.0Z" />
        <path d="M72.0 -10H84.0L86.0 494H70.0Z" />
        <path d="M151.6 -10H161.4L163.0 486H150.0Z" />
        <path d="M256.1 -10H268.9L271.0 488H254.0Z" />
        <path d="M297.8 -10H308.2L310.0 492H296.0Z" />
        <path d="M358.5 -10H373.5L376.0 504H356.0Z" />
      <path d="M161 306L198 296L198.6 298.4L161.6 310Z" />
    </g>
    <g className="nt-forest__bird" transform="translate(186 298.8)">
      <path d="M-5.2-3.2L-12.2 3L-10.8 4.2L-3.8-1.4Z" className="nt-forest__bird-tail" />
      <path d="M-6-2.8Q-4.4-8.8 1.6-8.6Q5.8-8.2 5.8-4.2Q5.2.4-.4.6Q-4.4.6-6-2.8Z" className="nt-forest__bird-back" />
      <path d="M-5.6-1.8Q-.6-2.6 5.7-4.4Q5.2.4-.4.6Q-4-.2-5.6-1.8Z" className="nt-forest__bird-breast" />
      <path d="M-5.4-3.4Q-1.4-7.6 3-6.2Q-.6-3.4-6.6-2.2Z" className="nt-forest__bird-tail" />
      <circle cx="3.8" cy="-8.4" r="2.8" className="nt-forest__bird-back" />
      <path d="M1.4-10.2Q3.8-11.8 6.2-9.6Q3.6-9.8 1.4-10.2Z" className="nt-forest__bird-patch" />
      <path d="M6.4-8.9L8.8-8.1L6.4-7.3Z" className="nt-forest__bird-beak" />
      <path d="M-1.4.4V1.6M.8.4V1.6" className="nt-forest__bird-legs" />
    </g>
    <rect y="528" width="400" height="140" fill={ref("mist")} />
    <g className="nt-forest__near">
        <path d="M-19.5 -10H29.5L40.0 660H-30.0Z" />
        <path d="M113.1 -10H136.9L142.0 610H108.0Z" />
        <path d="M299.8 -10H336.2L344.0 660H292.0Z" />
    </g>
    <rect y="500" width="400" height="140" fill={ref("fade")} />
  </svg>;
}
